// Copyright (c) 2016 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/clearcontainers/proxy/api"

	"github.com/golang/glog"
)

// Main struct holding the proxy state
type proxy struct {
	// Protect concurrent accesses from separate client goroutines to this
	// structure fields
	sync.Mutex

	// proxy socket
	listener   net.Listener
	socketPath string

	// vms are hashed by their containerID
	vms map[string]*vm

	// Output the VM console on stderr
	enableVMConsole bool

	wg sync.WaitGroup
}

// Represents a client, either a cc-oci-runtime or cc-shim process having
// opened a socket to the proxy
type client struct {
	id    uint64
	proxy *proxy
	vm    *vm

	conn net.Conn
}

func (c *client) info(lvl glog.Level, msg string) {
	if !glog.V(lvl) {
		return
	}
	glog.Infof("[client #%d] %s", c.id, msg)
}

func (c *client) infof(lvl glog.Level, fmt string, a ...interface{}) {
	if !glog.V(lvl) {
		return
	}
	a = append(a, 0)
	copy(a[1:], a[0:])
	a[0] = c.id
	glog.Infof("[client #%d] "+fmt, a...)
}

// "RegisterVM"
func registerVMHandler(data []byte, userData interface{}, response *handlerResponse) {
	client := userData.(*client)
	payload := api.RegisterVM{}

	if err := json.Unmarshal(data, &payload); err != nil {
		response.SetError(err)
		return
	}

	if payload.ContainerID == "" || payload.CtlSerial == "" || payload.IoSerial == "" {
		response.SetErrorMsg("malformed RegisterVM command")
	}

	proxy := client.proxy
	proxy.Lock()
	if _, ok := proxy.vms[payload.ContainerID]; ok {

		proxy.Unlock()
		response.SetErrorf("%s: container already registered",
			payload.ContainerID)
		return
	}

	client.infof(1,
		"RegisterVM(containerId=%s,ctlSerial=%s,ioSerial=%s,console=%s)",
		payload.ContainerID, payload.CtlSerial, payload.IoSerial,
		payload.Console)

	vm := newVM(payload.ContainerID, payload.CtlSerial, payload.IoSerial)
	proxy.vms[payload.ContainerID] = vm
	proxy.Unlock()

	if payload.Console != "" && proxy.enableVMConsole {
		vm.setConsole(payload.Console)
	}

	if err := vm.Connect(); err != nil {
		proxy.Lock()
		delete(proxy.vms, payload.ContainerID)
		proxy.Unlock()
		response.SetError(err)
		return
	}

	client.vm = vm

	// We start one goroutine per-VM to monitor the qemu process
	proxy.wg.Add(1)
	go func() {
		<-vm.OnVMLost()
		vm.Close()
		proxy.wg.Done()
	}()
}

// "attach"
func attachVMHandler(data []byte, userData interface{}, response *handlerResponse) {
	client := userData.(*client)
	proxy := client.proxy

	payload := api.AttachVM{}
	if err := json.Unmarshal(data, &payload); err != nil {
		response.SetError(err)
		return
	}

	proxy.Lock()
	vm := proxy.vms[payload.ContainerID]
	proxy.Unlock()

	if vm == nil {
		response.SetErrorf("unknown containerID: %s", payload.ContainerID)
		return
	}

	client.infof(1, "AttachVM(containerId=%s)", payload.ContainerID)

	client.vm = vm
}

// "UnregisterVM"
func unregisterVMHandler(data []byte, userData interface{}, response *handlerResponse) {
	// UnregisterVM only affects the proxy.vms map and so removes the VM
	// from the client visible API.
	// vm.Close(), which tears down the VM object, is done at the end of
	// the VM life cycle, when  we detect the qemu process is effectively
	// gone (see RegisterVMHandler)

	client := userData.(*client)
	proxy := client.proxy

	payload := api.UnregisterVM{}
	if err := json.Unmarshal(data, &payload); err != nil {
		response.SetError(err)
		return
	}

	proxy.Lock()
	vm := proxy.vms[payload.ContainerID]
	proxy.Unlock()

	if vm == nil {
		response.SetErrorf("unknown containerID: %s", payload.ContainerID)
		return
	}

	client.info(1, "UnregisterVM()")

	proxy.Lock()
	delete(proxy.vms, vm.containerID)
	proxy.Unlock()

	client.vm = nil
}

// "hyper"
func hyperHandler(data []byte, userData interface{}, response *handlerResponse) {
	client := userData.(*client)
	hyper := api.Hyper{}
	vm := client.vm

	if err := json.Unmarshal(data, &hyper); err != nil {
		response.SetError(err)
		return
	}

	if vm == nil {
		response.SetErrorMsg("client not attached to a vm")
		return
	}

	client.infof(1, "hyper(cmd=%s, data=%s)", hyper.HyperName, hyper.Data)

	err := vm.SendMessage(hyper.HyperName, hyper.Data)
	response.SetError(err)
}

func newProxy() *proxy {
	return &proxy{
		vms: make(map[string]*vm),
	}
}

// DefaultSocketPath is populated at link time with the value of:
//   ${locatestatedir}/run/cc-oci-runtime/proxy
var DefaultSocketPath string

// ArgSocketPath is populated at runtime from the option -socket-path
var ArgSocketPath = flag.String("socket-path", "", "specify path to socket file")

// getSocketPath computes the path of the proxy socket. Note that when socket
// activated, the socket path is specified in the systemd socket file but the
// same value is set in DefaultSocketPath at link time.
func getSocketPath() string {
	// Invoking "go build" without any linker option will not
	// populate DefaultSocketPath, so fallback to a reasonable
	// path. People should really use the Makefile though.
	if DefaultSocketPath == "" {
		DefaultSocketPath = "/var/run/cc-oci-runtime/proxy.sock"
	}

	socketPath := DefaultSocketPath

	if len(*ArgSocketPath) != 0 {
		socketPath = *ArgSocketPath
	}

	return socketPath
}

func (proxy *proxy) init() error {
	var l net.Listener
	var err error

	// flags
	v := flag.Lookup("v").Value.(flag.Getter).Get().(glog.Level)
	proxy.enableVMConsole = v >= 3

	// Open the proxy socket
	proxy.socketPath = getSocketPath()
	fds := listenFds()

	if len(fds) > 1 {
		return fmt.Errorf("too many activated sockets (%d)", len(fds))
	} else if len(fds) == 1 {
		fd := fds[0]
		l, err = net.FileListener(fd)
		if err != nil {
			return fmt.Errorf("couldn't listen on socket: %v", err)
		}

	} else {
		socketDir := filepath.Dir(proxy.socketPath)
		if err = os.MkdirAll(socketDir, 0750); err != nil {
			return fmt.Errorf("couldn't create socket directory: %v", err)
		}
		if err = os.Remove(proxy.socketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("couldn't remove exiting socket: %v", err)
		}
		l, err = net.ListenUnix("unix", &net.UnixAddr{Name: proxy.socketPath, Net: "unix"})
		if err != nil {
			return fmt.Errorf("couldn't create AF_UNIX socket: %v", err)
		}
		if err = os.Chmod(proxy.socketPath, 0660|os.ModeSocket); err != nil {
			return fmt.Errorf("couldn't set mode on socket: %v", err)
		}

		glog.V(1).Info("listening on ", proxy.socketPath)
	}

	proxy.listener = l

	return nil
}

var nextClientID = uint64(1)

func (proxy *proxy) serveNewClient(proto *protocol, newConn net.Conn) {
	newClient := &client{
		id:    nextClientID,
		proxy: proxy,
		conn:  newConn,
	}

	atomic.AddUint64(&nextClientID, 1)

	// Unfortunately it's hard to find out information on the peer
	// at the other end of a unix socket. We use a per-client ID to
	// identify connections.
	newClient.info(1, "client connected")

	if err := proto.Serve(newConn, newClient); err != nil && err != io.EOF {
		newClient.infof(1, "error serving client: %v", err)
	}

	newConn.Close()
	newClient.info(1, "connection closed")
}

func (proxy *proxy) serve() {

	// Define the client (runtime/shim) <-> proxy protocol
	proto := newProtocol()
	proto.Handle(api.CmdRegisterVM, registerVMHandler)
	proto.Handle(api.CmdAttachVM, attachVMHandler)
	proto.Handle(api.CmdUnregisterVM, unregisterVMHandler)
	proto.Handle(api.CmdHyper, hyperHandler)

	glog.V(1).Info("proxy started")

	for {
		conn, err := proxy.listener.Accept()
		if err != nil {
			fmt.Fprintln(os.Stderr, "couldn't accept connection:", err)
			continue
		}

		go proxy.serveNewClient(proto, conn)
	}
}

func proxyMain() {
	proxy := newProxy()
	if err := proxy.init(); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err.Error())
		os.Exit(1)
	}
	proxy.serve()

	// Wait for all the goroutines started by registerVMHandler to finish.
	//
	// Not stricly necessary as:
	//   • currently proxy.serve() cannot return,
	//   • even if it was, the process is about to exit anyway...
	//
	// That said, this wait group is used in the tests to ensure proper
	// serialisation between runs of proxyMain()(see proxy/proxy_test.go).
	proxy.wg.Wait()
}

func initLogging() {
	// We print logs on stderr by default.
	flag.Set("logtostderr", "true")

	// It can be practical to use an environment variable to trigger a verbose output
	level := os.Getenv("CC_PROXY_LOG_LEVEL")
	if level != "" {
		flag.Set("v", level)
	}
}

type profiler struct {
	enabled bool
	host    string
	port    uint
}

func (p *profiler) setup() {
	if !p.enabled {
		return
	}

	addr := fmt.Sprintf("%s:%d", p.host, p.port)
	url := "http://" + addr + "/debug/pprof"
	glog.V(1).Info("pprof enabled on " + url)

	go func() {
		http.ListenAndServe(addr, nil)
	}()
}

func main() {
	var pprof profiler

	initLogging()

	flag.BoolVar(&pprof.enabled, "pprof", false,
		"enable pprof ")
	flag.StringVar(&pprof.host, "pprof-host", "localhost",
		"host the pprof server will be bound to")
	flag.UintVar(&pprof.port, "pprof-port", 6060,
		"port the pprof server will be bound to")

	flag.Parse()
	defer glog.Flush()

	pprof.setup()
	proxyMain()
}
