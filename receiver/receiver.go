package receiver

import (
	"bytes"
	"fmt"
	"log"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/lomik/go-carbon/helper"
	"github.com/lomik/go-carbon/points"
)

type Receiver interface {
	Stop()
	Stat(helper.StatCallback)
}

type protocolRecord struct {
	newOptions  func() interface{}
	newReceiver func(name string, options interface{}, store func(*points.Points)) (Receiver, error)
}

var protocolMap = map[string]*protocolRecord{}
var protocolMapMutex sync.Mutex

func Register(protocol string,
	newOptions func() interface{},
	newReceiver func(name string, options interface{}, store func(*points.Points)) (Receiver, error)) {

	protocolMapMutex.Lock()
	defer protocolMapMutex.Unlock()

	_, ok := protocolMap[protocol]
	if ok {
		log.Fatalf("protocol %#v already registered", protocol)
	}

	protocolMap[protocol] = &protocolRecord{
		newOptions:  newOptions,
		newReceiver: newReceiver,
	}
}

func New(name string, opts map[string]interface{}, store func(*points.Points)) (Receiver, error) {
	protocolNameObj, ok := opts["protocol"]
	if !ok {
		return nil, fmt.Errorf("protocol unspecified")
	}

	protocolName, ok := protocolNameObj.(string)
	if !ok {
		fmt.Errorf("bad protocol option %#v", protocolNameObj)
	}

	protocolMapMutex.Lock()
	protocol, ok := protocolMap[protocolName]
	protocolMapMutex.Unlock()

	if !ok {
		return nil, fmt.Errorf("unknown protocol %#v", protocolName)
	}

	delete(opts, protocolName)

	buf := new(bytes.Buffer)
	encoder := toml.NewEncoder(buf)
	encoder.Indent = ""
	if err := encoder.Encode(opts); err != nil {
		return nil, err
	}

	options := protocol.newOptions()

	if _, err := toml.Decode(buf.String(), options); err != nil {
		return nil, err
	}

	return protocol.newReceiver(name, options, store)
}
