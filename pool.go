// 新建连接池文件
package plugo

import (
	"errors"
	"net/rpc"
	"sync"
	"time"
)

var ErrNoAvailableConn = errors.New("no available connection in pool")

type ConnPool struct {
	mu      sync.Mutex
	conns   []*rpc.Client
	maxSize int
	timeout time.Duration
}

func NewConnPool(maxSize int, timeout time.Duration) *ConnPool {
	return &ConnPool{
		maxSize: maxSize,
		timeout: timeout,
		conns:   make([]*rpc.Client, 0, maxSize),
	}
}

func (p *ConnPool) Get() (*rpc.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.conns) > 0 {
		client := p.conns[len(p.conns)-1]
		p.conns = p.conns[:len(p.conns)-1]
		return client, nil
	}

	return nil, ErrNoAvailableConn
}

func (p *ConnPool) Put(client *rpc.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.conns) < p.maxSize {
		p.conns = append(p.conns, client)
	}
}
