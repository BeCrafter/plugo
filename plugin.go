// Copyright 2015 Giulio Iotti. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

// Package plugo implements the basics for creating and running subprocesses
// as plugins.  The subprocesses will communicate via either TCP or Unix socket
// to implement an interface that mimics the standard RPC package.
package plugo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	errInvalidMessage      = ErrInvalidMessage(errors.New("Invalid ready message"))
	errRegistrationTimeout = ErrRegistrationTimeout(errors.New("Registration timed out"))
)

// PluginStatus 表示插件当前状态
type PluginStatus int

const (
	StatusStopped PluginStatus = iota
	StatusRunning
	StatusError
	StatusRestarting
)

// HealthCheck 定义健康检查配置
type HealthConfig struct {
	Interval    time.Duration // 健康检查间隔时间
	MaxRetries  int           // 最多重试检查次数
	RetryDelay  time.Duration // 重试检查间隔时间
	AutoRestart bool          // 启用自动重启
}

// AsyncResult 异步调用结果
type AsyncResult struct {
	Response interface{}
	Error    error
	Done     chan struct{}
}

// Represents a plugin. After being created the plugin is not started or ready to run.
//
// Additional configuration (ErrorHandler and Timeout) can be set after initialization.
//
// Use Start() to make the plugin available.
//
// 表示一个插件。创建后的插件尚未启动或准备就绪。
//
// 初始化后可以设置额外的配置（错误处理器和超时时间）。
//
// 使用 Start() 使插件可用。
//
// 1. 插件结构定义
type Plugin struct {
	exe         string        // 插件可执行文件路径
	proto       string        // 通信协议(unix/tcp)
	unixdir     string        // Unix socket 目录
	params      []string      // 启动参数
	initTimeout time.Duration // 初始化超时时间
	exitTimeout time.Duration // 退出超时时间
	handler     ErrorHandler  // 错误处理器
	running     bool          // 运行状态
	meta        meta          // 元数据
	objsCh      chan *objects // 对象通道
	connCh      chan *conn    // 连接通道
	killCh      chan *waiter  // 终止通道
	exitCh      chan struct{} // 退出通道

	status     PluginStatus
	health     HealthConfig
	retryCount int
	healthCh   chan struct{}

	pool    *ConnPool
	metrics *Metrics
}

// NewPlugin create a new plugin ready to be started, or returns an error if the initial setup fails.
//
// The first argument specifies the protocol. It can be either set to "unix" for communication on an
// ephemeral local socket, or "tcp" for network communication on the local host (using a random
// unprivileged port.)
//
// This constructor will panic if the proto argument is neither "unix" nor "tcp".
//
// The path to the plugin executable should be absolute. Any path accepted by the "exec" package in the
// standard library is accepted and the same rules for execution are applied.
//
// Optionally some parameters might be passed to the plugin executable.

// NewPlugin 创建一个准备启动的新插件，如果初始化设置失败则返回错误。
//
// 第一个参数指定通信协议。可以设置为 "unix" 以使用临时本地套接字通信，
// 或设置为 "tcp" 以使用本地主机上的网络通信（使用随机的非特权端口）。
//
// 如果 proto 参数既不是 "unix" 也不是 "tcp"，这个构造函数会触发 panic。
//
// 插件可执行文件的路径应该是绝对路径。任何被标准库中 "exec" 包接受的路径都可以使用，
// 并且执行时遵循相同的规则。
//
// 可以选择性地向插件可执行文件传递一些参数。
func NewPlugin(proto, path string, params ...string) *Plugin {
	if proto != "unix" && proto != "tcp" {
		panic("Invalid protocol. Specify 'unix' or 'tcp'.")
	}
	p := &Plugin{
		exe:         path,
		proto:       proto,
		params:      params,
		initTimeout: 2 * time.Second,
		exitTimeout: 2 * time.Second,
		handler:     NewDefaultErrorHandler(),
		meta:        meta("plugo" + randstr(5)),
		objsCh:      make(chan *objects),
		connCh:      make(chan *conn),
		killCh:      make(chan *waiter),
		exitCh:      make(chan struct{}),
		pool:        NewConnPool(10, 2*time.Second), // 默认连接池配置
		metrics:     &Metrics{startTime: time.Now()},
	}
	return p
}

// Set the error (and output) handler implementation.  Use this to set a custom implementation.
// By default, standard logging is used.  See ErrorHandler.
//
// Panics if called after Start.
func (p *Plugin) SetErrorHandler(h ErrorHandler) {
	if p.running {
		panic("Cannot call SetErrorHandler after Start")
	}
	p.handler = h
}

// Set the maximum time a plugin is allowed to start up and to shut down.  Empty timeout (zero)
// is not allowed, default will be used.
//
// Default is two seconds.
//
// Panics if called after Start.
func (p *Plugin) SetTimeout(t time.Duration) {
	if p.running {
		panic("Cannot call SetTimeout after Start")
	}
	if t == 0 {
		return
	}
	p.initTimeout = t
	p.exitTimeout = t
}

func (p *Plugin) SetSocketDirectory(dir string) {
	if p.running {
		panic("Cannot call SetSocketDirectory after Start")
	}
	p.unixdir = dir
}

// SetHealthCheck 设置健康检查配置
func (p *Plugin) SetHealthCheck(config HealthConfig) {
	if p.running {
		panic("Cannot set health check after Start")
	}
	p.health = config
}

// Default string representation
func (p *Plugin) String() string {
	return fmt.Sprintf("%s %s", p.exe, strings.Join(p.params, " "))
}

// startHealthCheck 启动健康检查
func (p *Plugin) startHealthCheck() {
	if p.health.Interval == 0 {
		return
	}

	p.healthCh = make(chan struct{})
	go func() {
		ticker := time.NewTicker(p.health.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := p.checkHealth(); err != nil {
					p.handleHealthError(err)
				}
			case <-p.healthCh:
				return
			}
		}
	}()
}

// checkHealth 执行插件健康检查
func (p *Plugin) checkHealth() error {
	// 创建一个简单的 ping 请求来检查插件是否响应
	var response string
	err := p.Call("Plugin.Ping", 0, &response)
	if err != nil {
		p.status = StatusError
		return fmt.Errorf("health check failed: %v", err)
	}

	// 检查响应是否正确
	if response != "pong" {
		p.status = StatusError
		return fmt.Errorf("invalid ping response: %s", response)
	}

	p.status = StatusRunning
	p.retryCount = 0 // 重置重试计数
	return nil
}

// handleHealthError 处理健康检查错误
func (p *Plugin) handleHealthError(err error) {
	p.handler.Error(err)

	if !p.health.AutoRestart {
		return
	}

	p.retryCount++
	if p.retryCount > p.health.MaxRetries {
		p.handler.Error(fmt.Errorf("plugin restart failed after %d attempts", p.retryCount))
		return
	}

	p.status = StatusRestarting
	p.handler.Print(fmt.Sprintf("attempting to restart plugin (attempt %d/%d)", p.retryCount, p.health.MaxRetries))

	// 停止当前实例
	p.Stop()

	// 等待指定的重试延迟
	time.Sleep(p.health.RetryDelay)

	// 重新启动插件
	p.Start()
}

// GetStatus 返回插件当前状态
func (p *Plugin) GetStatus() PluginStatus {
	return p.status
}

// CallAsync 异步RPC调用
func (p *Plugin) CallAsync(name string, args interface{}, resp interface{}) *AsyncResult {
	result := &AsyncResult{
		Response: resp,
		Done:     make(chan struct{}),
	}

	go func() {
		result.Error = p.Call(name, args, result.Response)
		close(result.Done)
	}()

	return result
}

// Start will execute the plugin as a subprocess. Start will return immediately. Any first call to the
// plugin will reveal eventual errors occurred at initialization.
//
// Calls subsequent to Start will hang until the plugin has been properly initialized.

// Start 会将插件作为子进程执行。Start 会立即返回。任何对插件的第一次调用
// 都会暴露初始化过程中发生的错误。
//
// 在 Start 之后的调用会阻塞，直到插件被正确初始化。
func (p *Plugin) Start() {
	p.running = true
	p.registerInternalMethods()
	go p.run()

	// 启动健康检查
	p.startHealthCheck()
}

// Stop attemps to stop cleanly or kill the running plugin, then will free all resources.
// Stop returns when the plugin as been shut down and related routines have exited.

// Stop 尝试干净地停止或强制终止运行中的插件，然后释放所有资源。
// Stop 会一直等待，直到插件被完全关闭且相关的协程都已退出才返回。
func (p *Plugin) Stop() {
	// 停止健康检查
	if p.healthCh != nil {
		close(p.healthCh)
	}

	// 1. 创建一个新的等待器(waiter)，用于同步停止操作
	wr := newWaiter()

	// 2. 发送终止信号到 killCh 通道
	p.killCh <- wr

	// 3. 等待插件进程完全终止
	wr.wait()

	// 4. 发送退出信号到 exitCh 通道，通知主事件循环退出
	p.exitCh <- struct{}{}

	p.status = StatusStopped
}

// Call performs an RPC call to the plugin. Prior to calling Call, the plugin must have been
// initialized by calling Start.
//
// Call will hang until a plugin has been initialized; it will return any error that happens
// either when performing the call or during plugin initialization via Start.
//
// Please refer to the "rpc" package from the standard library for more information on the
// semantics of this function.
//
// 对插件执行 RPC 调用。在调用 Call 之前，必须通过调用 Start 来初始化插件。
//
// Call 会一直阻塞直到插件完成初始化；如果在调用过程中或者通过 Start 进行插件初始化时发生任何错误，
// 都会返回相应的错误。
//
// 关于此函数的语义详情，请参考标准库中的 "rpc" 包。
func (p *Plugin) Call(name string, args interface{}, resp interface{}) error {
	start := time.Now()

	client, err := p.pool.Get()
	if err != nil {
		// 创建新连接
		conn := &conn{wr: newWaiter()}
		p.connCh <- conn
		conn.wr.wait()

		if conn.err != nil {
			p.metrics.recordCall(time.Since(start), conn.err)
			return conn.err
		}
		client = conn.client
	}

	err = client.Call(name, args, resp)
	p.pool.Put(client)
	p.metrics.recordCall(time.Since(start), err)

	return err
}

// Objects returns a list of the exported objects from the plugin. Exported objects used
// internally are not reported.
//
// Like Call, Objects returns any error happened on initialization if called after Start.
func (p *Plugin) Objects() ([]string, error) {
	// 1. 创建一个新的 objects 对象，包含一个等待器
	objects := &objects{wr: newWaiter()}

	// 2. 将 objects 对象发送到对象通道
	p.objsCh <- objects

	// 3. 等待对象列表获取完成
	objects.wr.wait()

	// 4. 返回对象列表和可能的错误
	return objects.list, objects.err
}

func (p *Plugin) registerInternalMethods() {
	Register(&Health{})
}

// type internalMethods struct{}

type Health struct{}

func (m *Health) Ping(_ int, reply *string) error {
	*reply = "pong"
	return nil
}

const internalObject = "PlugoRpc"

type conn struct {
	client *rpc.Client
	err    error
	wr     *waiter
}

type waiter struct {
	c chan struct{}
}

func newWaiter() *waiter {
	return &waiter{c: make(chan struct{})}
}

func (wr *waiter) wait() {
	<-wr.c
}

func (wr *waiter) done() {
	close(wr.c)
}

func (wr *waiter) reset() {
	wr.c = make(chan struct{})
}

type client struct {
	*rpc.Client
	secret string
}

func newClient(s string, conn io.ReadWriteCloser) *client {
	return &client{secret: s, Client: rpc.NewClient(conn)}
}

func (c *client) authenticate(w io.Writer) error {
	// 向服务端写入认证令牌，格式为 "Auth-Token: <secret>\n\n"
	_, err := io.WriteString(w, "Auth-Token: "+c.secret+"\n\n")
	return err
}

func dialAuthRpc(secret, network, address string, timeout time.Duration) (*rpc.Client, error) {
	// 1. 使用超时机制建立网络连接
	conn, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}

	// 2. 创建带有认证信息的客户端
	c := newClient(secret, conn)

	// 3. 进行认证
	if err := c.authenticate(conn); err != nil {
		return nil, err
	}

	// 4. 返回认证后的 RPC 客户端
	return c.Client, nil
}

type objects struct {
	list []string
	err  error
	wr   *waiter
}

// 2. 控制器结构
type ctrl struct {
	p    *Plugin  // 所属插件
	objs []string // 导出对象列表
	// Protocol and address for RPC
	proto, addr string // 协议和地址
	// Secret needed to connect to server
	secret string // 认证密钥
	// Unrecoverable error is used as response to calls after it happened.
	err error // 错误信息
	// This channel is an alias to p.connCh. It allows to
	// intermittedly process calls (only when we can handle them).
	connCh chan *conn // 连接通道
	// Same as above, but for objects requests
	objsCh chan *objects // 对象通道
	// Timeout on plugin startup time
	timeoutCh <-chan time.Time // 超时通道
	// Get notification from Wait on the subprocess
	waitCh chan error // 等待通道
	// Get output lines from subprocess
	linesCh chan string // 输出行通道
	// Respond to a routine waiting for this mail loop to exit.
	over *waiter // 结束等待器
	// Executable
	proc *os.Process // 进程信息
	// RPC client to subprocess
	client *rpc.Client // RPC 客户端
}

func newCtrl(p *Plugin, t time.Duration) *ctrl {
	return &ctrl{
		p:         p,
		timeoutCh: time.After(t),
		linesCh:   make(chan string),
		waitCh:    make(chan error),
	}
}

func (c *ctrl) fatal(err error) {
	c.err = err
	c.open()
	c.kill()
}

func (c *ctrl) isFatal() bool {
	return c.err != nil
}

func (c *ctrl) close() {
	c.connCh = nil
	c.objsCh = nil
}

func (c *ctrl) open() {
	c.connCh = c.p.connCh
	c.objsCh = c.p.objsCh
}

func (c *ctrl) ready(val string) bool {
	var err error

	// 1. 解析就绪消息
	if err := c.parseReady(val); err != nil {
		c.fatal(err)
		return false
	}

	// 2. 建立 RPC 连接
	c.client, err = dialAuthRpc(c.secret, c.proto, c.addr, c.p.initTimeout)
	if err != nil {
		c.fatal(err)
		return false
	}

	// 3. 清理临时 Unix socket 文件
	if c.proto == "unix" {
		if err := os.Remove(c.addr); err != nil {
			c.p.handler.Error(errors.New("Cannot remove temporary socket: " + err.Error()))
		}
	}

	// 4. 关闭初始化超时
	c.timeoutCh = nil

	return true
}

func (c *ctrl) readOutput(r io.Reader) {
	// 1. 创建一个新的 Scanner 来读取输入流
	scanner := bufio.NewScanner(r)

	// 2. 循环读取每一行输出
	for scanner.Scan() {
		// 3. 将读取到的每一行发送到 linesCh 通道
		c.linesCh <- scanner.Text()
	}
}

func (c *ctrl) waitErr(pidCh chan<- int, err error) {
	close(pidCh)
	c.waitCh <- err
}

func (c *ctrl) wait(pidCh chan<- int, exe string, params ...string) {
	// 1. 确保 waitCh 在函数结束时关闭
	defer close(c.waitCh)

	// 2. 创建命令对象
	cmd := exec.Command(exe, params...)

	// 3. 设置标准输出管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.waitErr(pidCh, err)
		return
	}

	// 4. 设置标准错误管道
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.waitErr(pidCh, err)
		return
	}

	// 5. 启动进程
	if err := cmd.Start(); err != nil {
		c.waitErr(pidCh, err)
		return
	}

	// 6. 发送进程 PID 并关闭 PID 通道
	pidCh <- cmd.Process.Pid
	close(pidCh)

	// 7. 读取进程输出
	c.readOutput(stdout)
	c.readOutput(stderr)

	// 8. 等待进程结束并发送结果
	c.waitCh <- cmd.Wait()
}

func (c *ctrl) kill() {
	// 1. 检查进程是否存在
	if c.proc == nil {
		return
	}

	// 2. 强制终止进程
	// 忽略错误，因为进程可能已经结束
	c.proc.Kill()

	// 3. 清理进程引用
	c.proc = nil
}

func (c *ctrl) parseReady(str string) error {
	// 1. 检查并解析协议部分
	if !strings.HasPrefix(str, "proto=") {
		return errInvalidMessage
	}
	str = str[6:] // 跳过 "proto=" 前缀
	s := strings.IndexByte(str, ' ')
	if s < 0 {
		return errInvalidMessage
	}
	proto := str[0:s]
	if proto != "unix" && proto != "tcp" {
		return errInvalidMessage
	}
	c.proto = proto

	// 2. 检查并解析地址部分
	str = str[s+1:]
	if !strings.HasPrefix(str, "addr=") {
		return errInvalidMessage
	}
	c.addr = str[5:] // 跳过 "addr=" 前缀

	return nil
}

// Copy the list of objects for the requestor
func (c *ctrl) objects() []string {
	// 1. 创建结果列表，长度为对象总数减1（排除内部对象）
	list := make([]string, len(c.objs)-1)

	// 2. 使用双指针遍历并过滤对象
	for i, j := 0, 0; i < len(c.objs); i++ {
		// 跳过内部对象（PlugoRpc）
		if c.objs[i] == internalObject {
			continue
		}
		// 将非内部对象添加到结果列表
		list[j] = c.objs[i]
		j = j + 1
	}

	return list
}

// 3. 主要运行逻辑
func (p *Plugin) run() {
	// 设置默认的 Unix socket 目录
	if p.unixdir == "" {
		p.unixdir = os.TempDir()
	}

	// 初始化参数
	params := []string{
		"-plugo:prefix=" + string(p.meta),
		"-plugo:proto=" + p.proto,
	}
	if p.proto == "unix" && p.unixdir != "" {
		params = append(params, "-plugo:unixdir="+p.unixdir)
	}
	for i := 0; i < len(p.params); i++ {
		params = append(params, p.params[i])
	}

	// 创建控制器
	c := newCtrl(p, p.initTimeout)

	// 启动进程并获取 PID
	pidCh := make(chan int)
	go c.wait(pidCh, p.exe, params...)
	pid := <-pidCh

	// 获取进程对象
	if pid != 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			c.proc = proc
		}
	}

	// 主事件循环
	for {
		select {
		case <-c.timeoutCh: // 处理超时
			c.fatal(errRegistrationTimeout)
		case r := <-c.connCh: // 处理连接请求
			if c.isFatal() {
				r.err = c.err
				r.wr.done()
				continue
			}

			r.client = c.client
			r.wr.done()
		case o := <-c.objsCh: // 处理对象请求
			if c.isFatal() {
				o.err = c.err
				o.wr.done()
				continue
			}

			o.list = c.objects()
			o.wr.done()
		case line := <-c.linesCh: // 处理输出行
			key, val := p.meta.parse(line)
			switch key {
			case "auth-token":
				c.secret = val
			case "fatal":
				if err := parseError(val); err != nil {
					c.fatal(err)
				} else {
					c.fatal(errors.New(val))
				}
			case "error":
				if err := parseError(val); err != nil {
					p.handler.Print(err)
				} else {
					p.handler.Print(errors.New(val))
				}
			case "objects":
				c.objs = strings.Split(val, ", ")
			case "ready":
				if !c.ready(val) {
					continue
				}
				// Start accepting calls
				c.open()
			default:
				p.handler.Print(line)
			}
		case wr := <-p.killCh: // 处理终止请求
			if c.waitCh == nil {
				wr.done()
				continue
			}

			// If we don't accept calls, kill immediately
			// 1. 如果无法接受调用，直接杀死进程
			if c.connCh == nil || c.client == nil {
				c.kill()
			} else {
				// Be sure to kill the process if it doesn't obey Exit.
				// 2. 否则尝试优雅退出，超时后强制杀死
				go func(pid int, t time.Duration) {
					<-time.After(t)

					if proc, err := os.FindProcess(pid); err == nil {
						proc.Kill()
					}
				}(pid, p.exitTimeout)

				c.client.Call(internalObject+".Exit", 0, nil)
			}

			if c.client != nil {
				c.client.Close()
			}

			// Do not accept calls
			c.close()

			// When wait on the subprocess is exited, signal back via "over"
			c.over = wr
		case err := <-c.waitCh: // 处理等待结果
			if err != nil {
				if _, ok := err.(*exec.ExitError); !ok {
					p.handler.Error(err)
				}
				c.fatal(err)
			}

			// Signal to whoever killed us (via killCh) that we are done
			if c.over != nil {
				c.over.done()
			}

			// 清理资源并通知等待者
			c.proc = nil
			c.waitCh = nil
			c.linesCh = nil
		case <-p.exitCh: // 处理退出请求
			return
		}
	}
}
