package workerproto

import "encoding/json"

type shutdownPacket struct {
	Type string `json:"type"`
}

type gracefulTerminationPacket struct {
	FinishTasks bool `json:"finish-tasks"`
}

func AddRemoteShutdown(c *Communicator) func() error {
	c.AddCapability("shutdown")
	return func() error {
		return c.Send(&shutdownPacket{Type: "shutdown"})
	}
}

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
