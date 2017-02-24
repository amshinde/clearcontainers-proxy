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

package api

import (
	"encoding/json"
)

// The RegisterVM payload is issued first after connecting to the proxy socket.
// It is used to let the proxy know about a new container on the system along
// with the paths go hyperstart's command and I/O channels (AF_UNIX sockets).
//
// Console can be used to indicate the path of a socket linked to the VM
// console. The proxy can output this data when asked for verbose output.
//
//  {
//    "id": "registerVM",
//    "data": {
//      "containerId": "756535dc6e9ab9b560f84c8...",
//      "ctlSerial": "/tmp/sh.hyper.channel.0.sock",
//      "ioSerial": "/tmp/sh.hyper.channel.1.sock"
//    }
//  }
type RegisterVM struct {
	ContainerID string `json:"containerId"`
	CtlSerial   string `json:"ctlSerial"`
	IoSerial    string `json:"ioSerial"`
	Console     string `json:"console,omitempty"`
}

// RegisterVMResult is the result from a successful RegisterVM.
//
//  {
//    "success": true,
//    "data": {
//      "version": 1
//    }
//  }
type RegisterVMResult struct {
	// The version of the proxy protocol
	Version int `json:"version"`
}

// The AttachVM payload can be used to associate clients to an already known
// VM. AttachVM cannot be issued if a RegisterVM for this container hasn't been
// issued beforehand.
//
//  {
//    "id": "attach",
//    "data": {
//      "containerId": "756535dc6e9ab9b560f84c8..."
//    }
//  }
type AttachVM struct {
	ContainerID string `json:"containerId"`
}

// AttachVMResult is the result from a successful AttachVM.
//
//  {
//    "success": true,
//    "data": {
//      "version": 1
//    }
//  }
type AttachVMResult struct {
	// The version of the proxy protocol
	Version int `json:"version"`
}

// The UnregisterVM payload does the opposite of what RegisterVM does,
// indicating to the proxy it should release resources created by RegisterVM
// for the container identified by containerId.
//
//  {
//    "id": "unregister",
//    "data": {
//      "containerId": "756535dc6e9ab9b560f84c8..."
//    }
//  }
type UnregisterVM struct {
	ContainerID string `json:"containerId"`
}

// The AllocateIo payload asks the proxy to allocate IO stream sequence numbers
// for use with the execcmd hyperstart command.
//
// A stream sequence number is a globally unique number identifying a data
// stream between hyperstart and clients. This is used to multiplex stdin,
// stdout and stderr of several processes onto a single channel. Sequence
// numbers are associated with a process running on the VM and used by the
// proxy to route I/O data to and from the corresponding client.
//
// One can allocate up to two streams with allocateIO. stdin and stdout use the
// same sequence number as they can be differentiated by the direction of the
// data. If wanting stderr as its own stream, a second sequence number needs to
// be allocated.
//
// The result of an allocateIO operation is encoded as an AllocateIoResult.
//
//  {
//    "id": "allocateIO",
//    "data": {
//      "nStreams": 2
//    }
//  }
type AllocateIo struct {
	NStreams int `json:"nStreams"`
}

// AllocateIoResult is the result from a successful allocateIO.
//
// The sequence numbers allocated are:
//   ioBase, ioBase + 1, ..., ioBase + nStreams - 1
//
// Those sequence numbers should then be used by a client to populate an
// "execcmd" hyperstart command.
//
// The AllocateIOResult response is followed by a file descriptor. This file
// descriptor is sent through the out of band data mechanism of AF_UNIX sockets
// along with a single byte, 'F'.
//
// The proxy will route the I/O streams with the sequence numbers allocated by
// this operation between that file descriptor and hyperstart.
//
//  {
//    "success": true,
//    "data": {
//      "ioBase": 1234
//    }
//  }
type AllocateIoResult struct {
	IoBase uint64 `json:"ioBase"`
}

// The Hyper payload will forward an hyperstart command to hyperstart.
//
//  {
//    "id": "hyper",
//    "data": {
//      "hyperName": "startpod",
//      "data": {
//        "hostname": "clearlinux",
//        "containers": [],
//        "shareDir": "rootfs"
//      }
//    }
//  }
type Hyper struct {
	HyperName string          `json:"hyperName"`
	Data      json.RawMessage `json:"data,omitempty"`
}
