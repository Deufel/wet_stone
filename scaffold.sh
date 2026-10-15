#!/usr/bin/env bash
set -euo pipefail

PROJECT="asm-http-server"
mkdir -p "$PROJECT/src"
cd "$PROJECT"

cat > src/server.asm << 'EOF'
DEFAULT REL
CPU X64
BITS 64

%define sys_write          1
%define sys_close          3
%define sys_socket        41
%define sys_accept        43
%define sys_bind          49
%define sys_listen        50
%define sys_setsockopt   54
%define sys_exit          60

%define STDOUT_FILENO      1
%define AF_INET            2
%define SOCK_STREAM        1
%define IPPROTO_IP         0
%define SOL_SOCKET         1
%define SO_REUSEADDR       2

section .data
so_reuseaddr:   dd 1
so_reuseaddr_len equ $ - so_reuseaddr

sockaddr_in:
    .family:    dw AF_INET
    .port:      db 0x1f, 0x40
    .host:      db 127, 0, 0, 1
    .padding:   dq 0
sockaddr_in_len equ $ - sockaddr_in

listening_msg:  db 'Listening on 127.0.0.1:8000', 10
listening_msg_len equ $ - listening_msg

http_msg:       db 'HTTP/1.1 200 OK', 13, 10
                db 'Content-Length: 12', 13, 10
                db 13, 10
                db 'Hello world!', 10
http_msg_len    equ $ - http_msg

section .text
global _start

_start:
    mov rax, sys_socket
    mov rdi, AF_INET
    mov rsi, SOCK_STREAM
    mov rdx, IPPROTO_IP
    syscall
    cmp rax, 0
    jl .error
    mov r12, rax

    mov rax, sys_setsockopt
    mov rdi, r12
    mov rsi, SOL_SOCKET
    mov rdx, SO_REUSEADDR
    lea r10, [rel so_reuseaddr]
    mov r8, so_reuseaddr_len
    syscall

    mov rax, sys_bind
    mov rdi, r12
    lea rsi, [rel sockaddr_in]
    mov rdx, sockaddr_in_len
    syscall
    cmp rax, 0
    jl .error

    mov rax, sys_listen
    mov rdi, r12
    mov rsi, 10
    syscall
    cmp rax, 0
    jl .error

    mov rax, sys_write
    mov rdi, STDOUT_FILENO
    lea rsi, [rel listening_msg]
    mov rdx, listening_msg_len
    syscall

.accept_loop:
    mov rax, sys_accept
    mov rdi, r12
    xor rsi, rsi
    xor rdx, rdx
    syscall
    cmp rax, 0
    jl .error
    mov r13, rax

    mov rax, sys_write
    mov rdi, r13
    lea rsi, [rel http_msg]
    mov rdx, http_msg_len
    syscall

    mov rax, sys_close
    mov rdi, r13
    syscall

    jmp .accept_loop

.error:
    mov rdi, rax
    mov rax, sys_exit
    syscall
EOF

cat > Makefile << 'EOF'
AS = nasm
LD = ld
ASFLAGS = -f elf64
LDFLAGS = -s

TARGET = server
SRC = src/server.asm

.PHONY: all clean run

all: $(TARGET)

$(TARGET): $(SRC)
	$(AS) $(ASFLAGS) $< -o $(TARGET).o
	$(LD) $(LDFLAGS) $(TARGET).o -o $(TARGET)

run: $(TARGET)
	./$(TARGET)

clean:
	rm -f $(TARGET) $(TARGET).o
EOF

cat > README.md << 'EOF'
# Minimal HTTP Server in x86-64 Assembly

A tiny educational HTTP server written entirely in x86-64 assembly for Linux.
It uses only raw syscalls (no libc) to create a socket, bind to 127.0.0.1:8000,
listen, accept connections, and return a simple HTTP/1.1 response.

## Requirements

- Linux x86-64
- NASM (`nasm`)
- GNU ld

## Build & Run

```bash
make
make run
