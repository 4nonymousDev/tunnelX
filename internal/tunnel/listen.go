package tunnel

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
)

// multiListener 把多个 net.Listener 合成一个，Accept 返回任一监听上的连接。
// 用途：Import 模式需同时监听 127.0.0.1 与 ::1。只绑其中一个时，浏览器访问
// http://localhost:PORT 若解析到另一个地址族会连接被拒，且请求根本到不了本
// 程序——日志中不会留下任何痕迹，故障表现为"什么都没发生"，极难排查。
// multiListener combines several net.Listeners and accepts from any of them.
// Import mode must listen on both 127.0.0.1 and ::1; otherwise localhost may resolve
// to the unbound family, causing an invisible refusal before the request reaches us.
type multiListener struct {
	lns  []net.Listener
	ch   chan acceptResult
	once sync.Once
	done chan struct{}
	addr net.Addr
}

type acceptResult struct {
	conn net.Conn
	err  error
}

// CheckListenPort 试绑一次指定端口，用于在用户填写配置时就发现问题，
// 而非等到隧道启动才失败——那时错误只出现在日志里，用户刚点完"确定"
// 还以为一切正常。
//
// 存在固有的竞态：这里能绑上，不代表隧道启动时仍能绑上。但它要拦的是
// "端口被 Windows 保留"这类稳定状态，而非瞬时占用，故足够有用。
// CheckListenPort attempts a bind while the user edits configuration, surfacing errors
// before tunnel startup. A race remains, but this is intended to catch stable states
// such as Windows-reserved ports rather than transient occupancy.
func CheckListenPort(port int) error {
	ln, err := listenLoopback(port)
	if err == nil {
		ln.Close()
		return nil
	}
	return fmt.Errorf("%s", explainBindError(port, unwrapBindErr(err)))
}

// unwrapBindErr 从 listenLoopback 的聚合错误中取出首个可识别的系统错误码，
// 供 explainBindError 判断成因。
// unwrapBindErr extracts a recognizable system error code from listenLoopback's joined error.
func unwrapBindErr(err error) error {
	var se syscall.Errno
	if errors.As(err, &se) {
		return se
	}
	return err
}

// listenLoopback 在指定端口上同时监听 IPv4 与 IPv6 环回地址。
// 至少绑定成功一个即视为成功——部分系统禁用了 IPv6，此时只有 IPv4 可用，
// 不应因此让隧道整体失败。两个都失败才返回错误。
// listenLoopback listens on both IPv4 and IPv6 loopback. One successful bind is enough
// because IPv6 may be disabled; it returns an error only if both fail.
func listenLoopback(port int) (net.Listener, error) {
	addrs := []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	}

	var lns []net.Listener
	var errs []error
	for _, a := range addrs {
		// 显式指定 tcp4 / tcp6，避免 Go 依据地址自行选择时只绑一个地址族。
		// Specify tcp4/tcp6 explicitly so Go cannot bind only one family implicitly.
		network := "tcp4"
		if a[0] == '[' {
			network = "tcp6"
		}
		ln, err := net.Listen(network, a)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		lns = append(lns, ln)
	}

	if len(lns) == 0 {
		return nil, errors.Join(errs...)
	}

	m := &multiListener{
		lns:  lns,
		ch:   make(chan acceptResult),
		done: make(chan struct{}),
		addr: lns[0].Addr(),
	}
	for _, ln := range lns {
		go m.pump(ln)
	}
	return m, nil
}

func (m *multiListener) pump(ln net.Listener) {
	for {
		c, err := ln.Accept()
		select {
		case m.ch <- acceptResult{conn: c, err: err}:
		case <-m.done:
			if c != nil {
				c.Close()
			}
			return
		}
		if err != nil {
			return
		}
	}
}

func (m *multiListener) Accept() (net.Conn, error) {
	select {
	case r := <-m.ch:
		return r.conn, r.err
	case <-m.done:
		return nil, net.ErrClosed
	}
}

func (m *multiListener) Close() error {
	var err error
	m.once.Do(func() {
		close(m.done)
		for _, ln := range m.lns {
			if cerr := ln.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	})
	return err
}

func (m *multiListener) Addr() net.Addr { return m.addr }
