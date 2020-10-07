package workerproto

import (
	"encoding/json"
	"fmt"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"strings"
)

type testPacket struct {
	Type string
	A int
}

var _ = Describe("Parse", func() {
	It("should parse a simple test message", func() {
		r := strings.NewReader(`
~{"type":"test","a":7}
`)
		err := Parse(log, r, func(t string, p []byte) error {
			Expect(t).To(Equal("test"))

			var packet testPacket
			err := json.Unmarshal(p, &packet)
			Expect(err).To(Succeed())
			Expect(packet.A).To(Equal(7))
			return nil
		})
		Expect(err).To(Succeed())
	})

	It("should propagate errors from the handler", func() {
		r := strings.NewReader(`~{"type":"test"}\n`)
		testErr := fmt.Errorf("Hello, world!")

		err := Parse(log, r, func(t string, p []byte) error {
			return testErr
		})
		Expect(err).To(Equal(testErr))
	})
})

