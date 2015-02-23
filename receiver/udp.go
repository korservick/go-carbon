package receiver

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lomik/go-carbon/points"

	"github.com/Sirupsen/logrus"
)

// UDP receive metrics from UDP socket
type UDP struct {
	out                chan *points.Points
	exit               chan bool
	graphPrefix        string
	metricsReceived    uint32
	incompleteReceived uint32
	errors             uint32
	conn               *net.UDPConn
}

// NewUDP create new instance of UDP
func NewUDP(out chan *points.Points) *UDP {
	return &UDP{
		out:  out,
		exit: make(chan bool),
	}
}

type incompleteRecord struct {
	deadline time.Time
	data     []byte
}

// incompleteStorage store incomplete lines
type incompleteStorage struct {
	Records   map[string]*incompleteRecord
	Expires   time.Duration
	NextPurge time.Time
	MaxSize   int
}

func newIncompleteStorage() *incompleteStorage {
	return &incompleteStorage{
		Records:   make(map[string]*incompleteRecord, 0),
		Expires:   5 * time.Second,
		MaxSize:   10000,
		NextPurge: time.Now().Add(time.Second),
	}
}

func (storage *incompleteStorage) store(addr string, data []byte) {
	storage.Records[addr] = &incompleteRecord{
		deadline: time.Now().Add(storage.Expires),
		data:     data,
	}
	storage.checkAndClear()
}

func (storage *incompleteStorage) pop(addr string) []byte {
	if record, ok := storage.Records[addr]; ok {
		delete(storage.Records, addr)
		if record.deadline.Before(time.Now()) {
			return nil
		}
		return record.data
	}
	return nil
}

func (storage *incompleteStorage) purge() {
	now := time.Now()
	for key, record := range storage.Records {
		if record.deadline.Before(now) {
			delete(storage.Records, key)
		}
	}
	storage.NextPurge = time.Now().Add(time.Second)
}

func (storage *incompleteStorage) checkAndClear() {
	if len(storage.Records) < storage.MaxSize {
		return
	}
	if storage.NextPurge.After(time.Now()) {
		return
	}
	storage.purge()
}

// Addr returns binded socket address. For bind port 0 in tests
func (rcv *UDP) Addr() net.Addr {
	if rcv.conn == nil {
		return nil
	}
	return rcv.conn.LocalAddr()
}

// SetGraphPrefix for internal cache metrics
func (rcv *UDP) SetGraphPrefix(prefix string) {
	rcv.graphPrefix = prefix
}

// Stat sends internal statistics to cache
func (rcv *UDP) Stat(metric string, value float64) {
	rcv.out <- points.OnePoint(
		fmt.Sprintf("%s%s", rcv.graphPrefix, metric),
		value,
		time.Now().Unix(),
	)
}

// Listen bind port. Receive messages and send to out channel
func (rcv *UDP) Listen(addr *net.UDPAddr) error {
	var err error
	rcv.conn, err = net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				metricsReceived := atomic.LoadUint32(&rcv.metricsReceived)
				atomic.AddUint32(&rcv.metricsReceived, -metricsReceived)
				rcv.Stat("udp.metricsReceived", float64(metricsReceived))

				incompleteReceived := atomic.LoadUint32(&rcv.incompleteReceived)
				atomic.AddUint32(&rcv.incompleteReceived, -incompleteReceived)
				rcv.Stat("udp.incompleteReceived", float64(incompleteReceived))

				errors := atomic.LoadUint32(&rcv.errors)
				atomic.AddUint32(&rcv.errors, -errors)
				rcv.Stat("udp.errors", float64(errors))

				logrus.WithFields(logrus.Fields{
					"metricsReceived":    metricsReceived,
					"incompleteReceived": incompleteReceived,
					"errors":             errors,
				}).Info("[udp] doCheckpoint()")

			case <-rcv.exit:
				rcv.conn.Close()
				return
			}
		}
	}()

	go func() {
		defer rcv.conn.Close()

		var buf [2048]byte

		var data *bytes.Buffer

		lines := newIncompleteStorage()

		for {
			rlen, peer, err := rcv.conn.ReadFromUDP(buf[:])
			if err != nil {
				if strings.Contains(err.Error(), "use of closed network connection") {
					break
				}
				atomic.AddUint32(&rcv.errors, 1)
				logrus.Error(err)
				continue
			}

			prev := lines.pop(peer.String())

			if prev != nil {
				data = bytes.NewBuffer(prev)
				data.Write(buf[:rlen])
			} else {
				data = bytes.NewBuffer(buf[:rlen])
			}

			for {
				line, err := data.ReadBytes('\n')

				if err != nil {
					if err == io.EOF {
						if len(line) > 0 {
							lines.store(peer.String(), line)
							atomic.AddUint32(&rcv.incompleteReceived, 1)
						}
					} else {
						atomic.AddUint32(&rcv.errors, 1)
						logrus.Error(err)
					}
					break
				}
				if len(line) > 0 { // skip empty lines
					if msg, err := points.ParseText(string(line)); err != nil {
						atomic.AddUint32(&rcv.errors, 1)
						logrus.Info(err)
					} else {
						atomic.AddUint32(&rcv.metricsReceived, 1)
						rcv.out <- msg
					}
				}
			}
		}

	}()

	return nil
}

// Stop all listeners
func (rcv *UDP) Stop() {
	close(rcv.exit)
}
