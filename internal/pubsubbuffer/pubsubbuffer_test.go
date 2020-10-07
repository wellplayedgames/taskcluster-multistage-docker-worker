package pubsubbuffer

import (
	"context"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
)

var _ = Describe("Buffer", func() {
	It("should return what is written", func() {
		testBytes := []byte("Hello, world!\n")

		b := NewBuffer()
		n, err := b.Write(testBytes)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(testBytes)))
		err = b.Close()
		Expect(err).To(Succeed())

		buffer := make([]byte, len(testBytes) + 10)
		r := b.Subscribe(context.Background())
		n, err = r.Read(buffer)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(testBytes)))
		Expect(buffer[:len(testBytes)]).To(Equal(testBytes))
		n, err = r.Read(buffer)
		Expect(err).To(Equal(io.EOF))
		Expect(n).To(Equal(0))
	})

	It("should keep all readers open until the buffer is closed", func() {
		testBytes := []byte("Hello, World!\n")

		b := NewBuffer()
		n, err := b.Write(testBytes)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(testBytes)))

		r := b.Subscribe(context.Background())
		ch := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(ch)
			by, err := ioutil.ReadAll(r)
			Expect(err).To(Succeed())
			Expect(by).To(Equal(testBytes))
		}()

		Consistently(ch).ShouldNot(BeClosed())
		b.Close()
		Eventually(ch).Should(BeClosed())
	})

	It("should return what is written over HTTP", func() {
		testBytes := []byte("Hello, world!\n")

		b := NewBuffer()
		n, err := b.Write(testBytes)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(testBytes)))
		err = b.Close()
		Expect(err).To(Succeed())

		req, err := http.NewRequest(http.MethodGet, "/", nil)
		Expect(err).To(Succeed())
		resp := httptest.NewRecorder()
		b.ServeHTTP(resp, req)

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Body.Bytes()).To(Equal(testBytes))
	})

	It("should keep all HTTP requests open until the buffer is closed", func() {
		testBytes := []byte("Hello, World!\n")

		b := NewBuffer()
		n, err := b.Write(testBytes)
		Expect(err).To(Succeed())
		Expect(n).To(Equal(len(testBytes)))

		ch := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(ch)

			req, err := http.NewRequest(http.MethodGet, "/", nil)
			Expect(err).To(Succeed())
			resp := httptest.NewRecorder()
			b.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(resp.Body.Bytes()).To(Equal(testBytes))
		}()

		Consistently(ch).ShouldNot(BeClosed())
		b.Close()
		Eventually(ch).Should(BeClosed())
	})
})

