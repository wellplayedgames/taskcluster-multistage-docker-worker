package pubsubbuffer

import (
	"net/http"
	"strings"
	"sync"

	"github.com/taskcluster/slugid-go/slugid"
)

// Server is a HTTP handler which can be used to securely host
// several buffers (read-only).
type Server struct {
	lock sync.Mutex
	buffers map[string]*Buffer
}

var _ http.Handler = (*Server)(nil)

func NewServer() *Server {
	return &Server{
		buffers: map[string]*Buffer{},
	}
}

func (s *Server) getBuffer(id string) *Buffer {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.buffers[id]
}

// ServerHTTP implements HTTP for the server.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	pathParts := strings.Split(request.URL.Path[1:], "/")

	if pathParts[len(pathParts) - 1] == "" {
		pathParts = pathParts[:len(pathParts) - 1]
	}

	if len(pathParts) != 1 {
		writer.WriteHeader(http.StatusNotFound)
		writer.Write([]byte("Not Found"))
		return
	}

	buffer := s.getBuffer(pathParts[0])
	if buffer == nil {
		writer.WriteHeader(http.StatusNotFound)
		writer.Write([]byte("Not Found"))
		return
	}

	buffer.ServeHTTP(writer, request)
}

// Allocate creates a new buffer, assigns it a URL and returns a writer.
func (s *Server) Allocate() (string, WriteSubscribeCloser) {
	id := slugid.Nice() + slugid.V4()
	b := NewBuffer()
	w := &writer{
		Buffer: b,
		Server: s,
		id: id,
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	s.buffers[id] = b
	return id, w
}

type writer struct {
	*Buffer
	*Server
	id string
}

func (w *writer) Close() error {
	w.Buffer.Close()

	w.Server.lock.Lock()
	defer w.Server.lock.Unlock()
	delete(w.Server.buffers, w.id)
	return nil
}
