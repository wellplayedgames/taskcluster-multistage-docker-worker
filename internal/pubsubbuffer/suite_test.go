package pubsubbuffer

import (
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"go.uber.org/zap"
	"testing"
)

var log logr.Logger

func TestSuite(t *testing.T) {
	l, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	log = zapr.NewLogger(l)

	RegisterFailHandler(Fail)
	RunSpecs(t, "pubsubbuffer suite")
}
