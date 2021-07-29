package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRestClient(t *testing.T) {
	client := NewRestClient()
	assert.NotNil(t, client)
	assert.NotNil(t, client.restClient)

	addr := "ahgakgkajn"
	client.SetAddr(addr)
	assert.Equal(t, addr, client.Addr)
}
