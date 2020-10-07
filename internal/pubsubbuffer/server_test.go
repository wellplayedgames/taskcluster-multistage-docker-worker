package pubsubbuffer

import (
	"fmt"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"net/http"
	"net/http/httptest"
	"time"
)

var _ = Describe("Server", func() {
	It("should return Not Found for non-existent buffers", func() {
		server := NewServer()

		req, err := http.NewRequest(http.MethodGet, "/i-dont-exist", nil)
		Expect(err).To(Succeed())

		resp := httptest.NewRecorder()
		server.ServeHTTP(resp, req)

		Expect(resp.Code).To(Equal(http.StatusNotFound))
	})

	It("should return the contents of allocated buffers", func() {
		contents := []byte("Hello, world!\n")

		server := NewServer()
		id, b := server.Allocate()
		Expect(id).ToNot(BeEmpty())
		n, err := b.Write(contents)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(contents)))

		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("/%s", id), nil)
		Expect(err).To(Succeed())
		resp := httptest.NewRecorder()

		go func() {
			defer GinkgoRecover()
			time.Sleep(10 * time.Millisecond)
			err = b.Close()
			Expect(err).To(Succeed())
		}()
		server.ServeHTTP(resp, req)

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Body.Bytes()).To(Equal(contents))
	})
})

