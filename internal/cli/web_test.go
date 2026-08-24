package cli

import (
	"testing"
)

func TestWebCommandRegistered(t *testing.T) {
	root := NewRootCmd(chatTestDeps(newStubChatService(nil)))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "web" {
			found = true
			if c.Short == "" {
				t.Fatal("web 命令缺少描述")
			}
		}
	}
	if !found {
		t.Fatal("缺少 web 子命令")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	if !isLoopbackAddr("127.0.0.1:8081") || !isLoopbackAddr(":8081") || !isLoopbackAddr("localhost:8081") {
		t.Fatal("回环地址误判")
	}
	if isLoopbackAddr("0.0.0.0:8081") || isLoopbackAddr("192.168.1.5:8081") {
		t.Fatal("非回环地址误判")
	}
}
