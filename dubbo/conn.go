package dubbo

import (
	"net"
	"errors"
	"context"
	"time"
	"sync/atomic"

	"dubbo-mesh/log"
	"dubbo-mesh/derror"
	"dubbo-mesh/json"
)

var (
	ReadBeforeRequestError = errors.New("")
	PoolShutdownError      = errors.New("pool shutdown")
	ParamSeparator         = []byte("\n")
)

const (
	BufSize = 8192
)

// TODO 心跳
// 每次只会被一个goroutine获取，不会有并发问题
type Conn struct {
	net.Conn
	send bool
	buf  []byte
}

func (this *Conn) WriteRequest(req *Request) (err error) {
	header := headerPool.Get().(Header)
	defer headerPool.Put(header)
	header[2] = FlagRequest | 6
	if req.TwoWay {
		header[2] |= FlagTwoWay
	}
	if req.Event {
		header[2] |= FlagEvent
	}
	EncodeInt64(header, req.Id, 4)
	data := EncodeInvocation(req.Data.(*Invocation))
	EncodeInt(header, len(data), 12)
	_, err = this.Write(header)
	_, err = this.Write(data)
	if err != nil {
		return
	}
	this.send = true
	return
}

func (this *Conn) Close() (err error) {
	log.Info("close ", this.RemoteAddr())
	return this.Conn.Close()
}

// 暂时不判断是否是请求，跑benchmark不会很久
func (this *Conn) ReadResponse() (resp *Response, err error) {
	if !this.send {
		err = ReadBeforeRequestError
		return
	}
	header := headerPool.Get().(Header)
	defer headerPool.Put(header)
	_, err = this.Read(header)
	if err != nil {
		return
	}
	length := header.DataLen()
	var data []byte
	if length > BufSize {
		data = make([]byte, length)
	} else {
		data = this.buf[:length]
	}
	_, err = this.Read(data)
	if err != nil {
		return
	}
	resp = NewResponse(header.Status(), header.RequestId(), data)
	this.send = false
	return
}

func (this *Conn) HeartBeat(header Header) (err error) {
	header[2] |= 0 | FlagTwoWay | 6
	header[3] = StatusOk
	data, _ := json.Marshal(nil)
	EncodeInt(header, len(data), 12)
	_, err = this.Write(header)
	_, err = this.Write(data)
	return err
}

func NewPool(max int, dubbo string) *Pool {
	log.Info("dubbo", dubbo)
	pool := &Pool{addr: dubbo, ch: make(chan *Conn, max)}
	return pool
}

type Pool struct {
	addr  string
	count uint32
	ch    chan *Conn
}

func (this *Pool) new() (*Conn, error) {
	conn, err := net.Dial("tcp", this.addr)

	if err != nil {
		return nil, err
	}
	count := atomic.AddUint32(&this.count, 1)
	log.Infof("new %d,%s", count, conn.RemoteAddr())
	return &Conn{Conn: conn, buf: make([]byte, BufSize)}, nil
}

func (this *Pool) Get() (*Conn, error) {
	select {
	case conn, more := <-this.ch:
		if !more {
			return nil, PoolShutdownError
		}
		return conn, nil
	default:
		return this.new()
	}
}

func (this *Pool) Put(conn *Conn) {
	select {
	case this.ch <- conn:
	default:
		conn.Close()
	}
}

func (this *Pool) Shutdown() error {
	ctx, _ := context.WithTimeout(context.Background(), 1*time.Minute)
	close(this.ch)
	done := make(chan struct{})
	go func() {
		for conn := range this.ch {
			derror.Warn(conn.Close())
		}
		done <- struct{}{}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
