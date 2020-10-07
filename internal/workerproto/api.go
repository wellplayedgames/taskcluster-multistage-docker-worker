package workerproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-logr/logr"
	"io"
	"sync"
)

// MessageHandler is used to implement a single workerproto packet.
type MessageHandler = func(packet []byte) error

// Communicator runs a duplex stream to the worker-runner.
type Communicator struct {
	localCapabilities  map[string]struct{}
	remoteCapabilities map[string]struct{}
	handlers           map[string]MessageHandler

	capsLock sync.Mutex

	isWorker bool
	writer   io.Writer
	log      logr.Logger
}

// NewCommunicator creates a new communicator given the log and whather the
// communicator should act as the worker or runner.
//
// Run must be called with the input and output streams to manage the
// communicator.
func NewCommunicator(log logr.Logger, isWorker bool) *Communicator {
	c := Communicator{
		localCapabilities:  map[string]struct{}{},
		remoteCapabilities: map[string]struct{}{},
		handlers:           map[string]MessageHandler{},

		isWorker: isWorker,
		writer:   bytes.NewBuffer(nil),
		log:      log,
	}

	// Built-in handlers
	c.AddHandler("welcome", c.onWelcome)
	c.AddHandler("hello", c.onHello)

	return &c
}

// HasCapability returns true if both ends support a capability.
func (c *Communicator) HasCapability(cap string) bool {
	c.capsLock.Lock()
	defer c.capsLock.Unlock()
	_, hasLocal := c.localCapabilities[cap]
	_, hasRemote := c.remoteCapabilities[cap]
	return hasLocal && hasRemote
}

// AddCapability marks this communicator as supporting a given capability.
//
// It must not be called after Run() has been called.
func (c *Communicator) AddCapability(cap string) {
	c.localCapabilities[cap] = struct{}{}
}

// AddHandler adds a handler for a given packet type.
//
// It must not be called after Run() has been called.
func (c *Communicator) AddHandler(packetType string, handler MessageHandler) {
	c.handlers[packetType] = handler
}

// Send a packet over the communicator.
func (c *Communicator) Send(packet interface{}) error {
	return Write(c.writer, packet)
}

// Run this communicator until the streams are closed or an error occurs.
func (c *Communicator) Run(r io.Reader, w io.Writer) error {
	oldWriter := c.writer
	c.writer = w

	if !c.isWorker {
		caps := make([]string, 0, len(c.localCapabilities))
		for k, _ := range c.localCapabilities {
			caps = append(caps, k)
		}

		err := c.Send(&helloPacket{
			Type:         "welcome",
			Capabilities: caps,
		})
		if err != nil {
			return err
		}
	}

	if ow, ok := oldWriter.(*bytes.Buffer); ok {
		_, err := io.Copy(w, ow)
		if err != nil {
			return err
		}
	}

	return Parse(c.log, r, c.handle)
}

func (c *Communicator) handle(packetType string, packet []byte) error {
	handler := c.handlers[packetType]
	if handler == nil {
		return fmt.Errorf("unexpected packet received: %s", string(packet))
	}

	return handler(packet)
}

// Packet handlers

type helloPacket struct {
	Type         string   `json:"type"`
	Capabilities []string `json:"capabilities"`
}

func (c *Communicator) onWelcome(packet []byte) error {
	err := c.onHello(packet)
	if err != nil {
		return err
	}

	caps := make([]string, 0, len(c.localCapabilities))
	for k, _ := range c.localCapabilities {
		caps = append(caps, k)
	}

	return c.Send(&helloPacket{
		Type:         "hello",
		Capabilities: caps,
	})
}

func (c *Communicator) onHello(packet []byte) error {
	var p helloPacket
	err := json.Unmarshal(packet, &p)
	if err != nil {
		return err
	}

	c.log.Info("Connected workerproto",
		"localCapabilities", c.localCapabilities,
		"remoteCapabilities", p.Capabilities)

	c.capsLock.Lock()
	defer c.capsLock.Unlock()

	for _, cap := range p.Capabilities {
		c.remoteCapabilities[cap] = struct{}{}
	}

	return nil
}
