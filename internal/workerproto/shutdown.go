package workerproto

import "encoding/json"

type shutdownPacket struct {
	Type string `json:"type"`
}

type gracefulTerminationPacket struct {
	FinishTasks bool `json:"finish-tasks"`
}

// AddRemoteShutdown adds worker support for being remotely shutdown by
// the runner.
func AddRemoteShutdown(c *Communicator) func() error {
	c.AddCapability("shutdown")
	return func() error {
		return c.Send(&shutdownPacket{Type: "shutdown"})
	}
}

// AddGracefulTermination adds worker support for being asked to exit
// gracefully.
func AddGracefulTermination(c *Communicator) <-chan bool {
	ch := make(chan bool, 1)

	c.AddCapability("graceful-termination")
	c.AddHandler("graceful-termination", func(packet []byte) error {
		defer close(ch)

		var p gracefulTerminationPacket
		err := json.Unmarshal(packet, &p)
		if err != nil {
			return err
		}

		ch <- p.FinishTasks
		return nil
	})

	return ch
}
