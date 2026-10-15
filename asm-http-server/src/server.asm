; ---------------------------------------------------------------------------
; Personal file server + live feed, in x86-64 assembly (Linux, no libc)
;
;   ./server DIR                  serve DIR on 127.0.0.1:8000
;   ./server DIR <(cmd ...)       also stream cmd's output lines to the feed
;   ./server DIR <(cmd ...) wait  same, but cmd is paused until a browser connects
;
;   GET    /path/      directory listing page (HTML + Datastar)
;   GET    /path       file download (sendfile), 301 to /path/ for directories
;   PUT    /path       upload (Content-Length body), 201
;   DELETE /path       unlink, 204
;   GET    /_events    Server-Sent Events feed in Datastar's format:
;                        activity lines  -> datastar-patch-elements (append to #log)
;                        "NN% ..." lines -> datastar-patch-signals {"pct":NN,"cur":"..."}
;
; One process, one thread, epoll. File transfers (downloads and uploads) are
; forked so a slow client never blocks the loop. Paths are percent-decoded and
; any ".." segment is rejected; the process chdir()s into DIR and only opens
; relative paths. No TLS, no auth: put Caddy or similar in front for the web.
; ---------------------------------------------------------------------------
DEFAULT REL
CPU X64
BITS 64

%define sys_read            0
%define sys_write           1
%define sys_open            2
%define sys_close           3
%define sys_fstat           5
%define sys_rt_sigaction   13
%define sys_sendfile       40
%define sys_socket         41
%define sys_accept         43
%define sys_sendto         44
%define sys_bind           49
%define sys_listen         50
%define sys_setsockopt     54
%define sys_fork           57
%define sys_exit           60
%define sys_chdir          80
%define sys_unlink         87
%define sys_getdents64    217
%define sys_exit_group    231
%define sys_epoll_wait    232
%define sys_epoll_ctl     233
%define sys_epoll_create1 291

%define STDOUT_FILENO       1
%define STDERR_FILENO       2
%define AF_INET             2
%define SOCK_STREAM         1
%define IPPROTO_IP          0
%define SOL_SOCKET          1
%define SO_REUSEADDR        2
%define MSG_NOSIGNAL   0x4000
%define O_RDONLY            0
%define O_WRONLY            1
%define O_CREAT          0x40
%define O_TRUNC         0x200
%define SIGPIPE            13
%define SIGCHLD            17
%define SIG_IGN             1

%define S_IFMT         0xF000
%define S_IFDIR        0x4000
%define DT_DIR              4
%define ST_MODE_OFF        24
%define ST_SIZE_OFF        48

%define EPOLLIN             1
%define EPOLLRDHUP     0x2000
%define EPOLL_CTL_ADD       1
%define EPOLL_CTL_DEL       2
%define EPOLL_CTL_MOD       3

%define TAG_LISTEN          0
%define TAG_SOURCE          1
%define TAG_PENDING         2
%define TAG_SSE             3

%define METHOD_GET          1
%define METHOD_PUT          2
%define METHOD_DELETE       3

%define MAX_EVENTS         16
%define MAX_CLIENTS        64
%define REQ_BUF_SIZE     8192
%define PATH_BUF_SIZE    4096
%define IN_BUF_SIZE      4096
%define XFER_BUF_SIZE   65536
%define DENTS_BUF_SIZE  32768
%define OUT_BUF_SIZE   262144
%define LOG_BUF_SIZE     8192
%define SENDFILE_MAX 0x7ffff000

; ---------------------------------------------------------------------------
section .data
so_reuseaddr:   dd 1
so_reuseaddr_len equ $ - so_reuseaddr

sockaddr_in:
    .family:    dw AF_INET
    .port:      db 0x1f, 0x40          ; 8000, network byte order
    .host:      db 127, 0, 0, 1
    .padding:   dq 0
sockaddr_in_len equ $ - sockaddr_in

sigign_act:     dq SIG_IGN, 0, 0, 0     ; sa_handler, sa_flags, sa_restorer, sa_mask

usage_msg:      db 'usage: ./server DIR [source-pipe] [wait]', 10
usage_msg_len   equ $ - usage_msg
listening_msg:  db 'Serving on http://127.0.0.1:8000', 10
listening_msg_len equ $ - listening_msg
source_done_msg: db 'source reached EOF; sent done signal to all clients', 10
source_done_msg_len equ $ - source_done_msg

events_path:    db '_events'
events_path_len equ $ - events_path
hdr_content_length: db 'Content-Length'
hdr_content_length_len equ $ - hdr_content_length
hdr_expect:     db 'Expect'
hdr_expect_len  equ $ - hdr_expect

page_head:      incbin "page_head.html"
page_head_len   equ $ - page_head
page_tail:      incbin "page_tail.html"
page_tail_len   equ $ - page_tail

html_hdr:       db 'HTTP/1.1 200 OK', 13, 10
                db 'Content-Type: text/html; charset=utf-8', 13, 10
                db 'Connection: close', 13, 10, 13, 10
html_hdr_len    equ $ - html_hdr

sse_hdr:        db 'HTTP/1.1 200 OK', 13, 10
                db 'Content-Type: text/event-stream', 13, 10
                db 'Cache-Control: no-cache', 13, 10
                db 'Connection: keep-alive', 13, 10, 13, 10
sse_hdr_len     equ $ - sse_hdr

r100:           db 'HTTP/1.1 100 Continue', 13, 10, 13, 10
r100_len        equ $ - r100

%macro RESP 3                           ; label, status line, body
%1: db 'HTTP/1.1 ', %2, 13, 10, 'Content-Type: text/plain', 13, 10, 'Connection: close', 13, 10, 13, 10, %3, 10
%1_len equ $ - %1
%endmacro
RESP r201, '201 Created',             'created'
RESP r204, '204 No Content',          ''
RESP r400, '400 Bad Request',         'bad request'
RESP r404, '404 Not Found',           'not found'
RESP r405, '405 Method Not Allowed',  'method not allowed'
RESP r411, '411 Length Required',     'Content-Length required'
RESP r500, '500 Internal Server Error', 'cannot open target'

s200: db '200'
s201: db '201'
s204: db '204'
s301: db '301'
s400: db '400'
s404: db '404'
s405: db '405'
s411: db '411'
s500: db '500'

m_get:          db 'GET'
m_put:          db 'PUT'
m_delete:       db 'DELETE'
m_other:        db '???'

f200_a:         db 'HTTP/1.1 200 OK', 13, 10, 'Content-Type: '
f200_a_len      equ $ - f200_a
f200_b:         db 13, 10, 'Content-Length: '
f200_b_len      equ $ - f200_b
f200_c:         db 13, 10, 'Connection: close', 13, 10, 13, 10
f200_c_len      equ $ - f200_c

r301_a:         db 'HTTP/1.1 301 Moved Permanently', 13, 10, 'Location: '
r301_a_len      equ $ - r301_a
r301_b:         db '/', 13, 10, 'Connection: close', 13, 10, 13, 10
r301_b_len      equ $ - r301_b

li_dir_a:       db '<li class="dir"><a href="'
li_dir_a_len    equ $ - li_dir_a
li_dir_b:       db '/">'
li_dir_b_len    equ $ - li_dir_b
li_dir_c:       db '/</a></li>', 10
li_dir_c_len    equ $ - li_dir_c
li_file_a:      db '<li><a href="'
li_file_a_len   equ $ - li_file_a
li_file_b:      db '">'
li_file_b_len   equ $ - li_file_b
li_file_c:      db '</a> <button onclick="del(this.previousElementSibling.textContent)" title="delete">&times;</button></li>', 10
li_file_c_len   equ $ - li_file_c
li_up:          db '<li class="dir"><a href="../">../</a></li>', 10
li_up_len       equ $ - li_up
li_trunc:       db '<li>&hellip; listing truncated</li>', 10
li_trunc_len    equ $ - li_trunc
h2_a:           db '<h2>/'
h2_a_len        equ $ - h2_a
h2_b:           db '</h2>', 10, '<ul id="files">', 10
h2_b_len        equ $ - h2_b
ul_end:         db '</ul>', 10
ul_end_len      equ $ - ul_end

; Datastar event fragments
sig_head:       db 'event: datastar-patch-signals', 10, 'data: signals {"pct":'
sig_head_len    equ $ - sig_head
sig_mid:        db ',"cur":"'
sig_mid_len     equ $ - sig_mid
sig_tail:       db '"}', 10, 10
sig_tail_len    equ $ - sig_tail
el_head:        db 'event: datastar-patch-elements', 10, 'data: mode append', 10
                db 'data: selector #log', 10, 'data: elements <div>'
el_head_len     equ $ - el_head
el_tail:        db '</div>', 10, 10
el_tail_len     equ $ - el_tail
done_msg:       db 'event: datastar-patch-signals', 10
                db 'data: signals {"done":true,"pct":100}', 10, 10
done_msg_len    equ $ - done_msg

log_bytes_a:    db ' ('
log_bytes_b:    db ' bytes)'
log_bytes_b_len equ $ - log_bytes_b

; MIME table: 8-byte zero-padded lowercase extension, pointer, length
mime_html:      db 'text/html; charset=utf-8'
mime_html_len   equ $ - mime_html
mime_text:      db 'text/plain; charset=utf-8'
mime_text_len   equ $ - mime_text
mime_css:       db 'text/css'
mime_css_len    equ $ - mime_css
mime_js:        db 'application/javascript'
mime_js_len     equ $ - mime_js
mime_json:      db 'application/json'
mime_json_len   equ $ - mime_json
mime_png:       db 'image/png'
mime_png_len    equ $ - mime_png
mime_jpg:       db 'image/jpeg'
mime_jpg_len    equ $ - mime_jpg
mime_gif:       db 'image/gif'
mime_gif_len    equ $ - mime_gif
mime_webp:      db 'image/webp'
mime_webp_len   equ $ - mime_webp
mime_svg:       db 'image/svg+xml'
mime_svg_len    equ $ - mime_svg
mime_pdf:       db 'application/pdf'
mime_pdf_len    equ $ - mime_pdf
mime_mp4:       db 'video/mp4'
mime_mp4_len    equ $ - mime_mp4
mime_webm:      db 'video/webm'
mime_webm_len   equ $ - mime_webm
mime_mp3:       db 'audio/mpeg'
mime_mp3_len    equ $ - mime_mp3
mime_zip:       db 'application/zip'
mime_zip_len    equ $ - mime_zip
mime_bin:       db 'application/octet-stream'
mime_bin_len    equ $ - mime_bin

%macro MIME 2
%%s: db %1
%%e:
    times 8 - (%%e - %%s) db 0
    dq %2, %2_len
%endmacro
mime_table:
    MIME 'html', mime_html
    MIME 'htm',  mime_html
    MIME 'txt',  mime_text
    MIME 'md',   mime_text
    MIME 'log',  mime_text
    MIME 'csv',  mime_text
    MIME 'css',  mime_css
    MIME 'js',   mime_js
    MIME 'json', mime_json
    MIME 'png',  mime_png
    MIME 'jpg',  mime_jpg
    MIME 'jpeg', mime_jpg
    MIME 'gif',  mime_gif
    MIME 'webp', mime_webp
    MIME 'svg',  mime_svg
    MIME 'pdf',  mime_pdf
    MIME 'mp4',  mime_mp4
    MIME 'webm', mime_webm
    MIME 'mp3',  mime_mp3
    MIME 'zip',  mime_zip
    dq 0, 0, 0

; ---------------------------------------------------------------------------
section .bss
ev:             resb 12
events:         resb 12 * MAX_EVENTS
req_buf:        resb REQ_BUF_SIZE
req_len:        resq 1
path_buf:       resb PATH_BUF_SIZE + 8  ; decoded, NUL-terminated, relative to DIR
path_len:       resq 1
raw_path_ptr:   resq 1
raw_path_len:   resq 1
raw_ends_slash: resq 1
method_id:      resq 1
body_len:       resq 1
body_off:       resq 1
file_fd:        resq 1
file_size:      resq 1
hdr_len:        resq 1
out_ptr:        resq 1
dent_ptr:       resq 1
dent_end:       resq 1
stat_buf:       resb 144
in_buf:         resb IN_BUF_SIZE
xfer_buf:       resb XFER_BUF_SIZE
dents_buf:      resb DENTS_BUF_SIZE
out_buf:        resb OUT_BUF_SIZE
log_buf:        resb LOG_BUF_SIZE
clients:        resq MAX_CLIENTS
nclients:       resq 1
pending_len:    resq 1
wait_mode:      resq 1
buf_total:      resq 1
line_start:     resq 1
scan_pos:       resq 1

; ---------------------------------------------------------------------------
section .text
global _start

; Globals: r12 = listening socket, r13 = source fd (-1 = none), r14 = epoll fd,
;          r15 = index into current epoll batch, rbp = batch size,
;          rbx = fd of the connection being handled.

_start:
    mov rax, [rsp]                  ; argc
    cmp rax, 2
    jge .have_dir
    mov rax, sys_write
    mov rdi, STDERR_FILENO
    lea rsi, [usage_msg]
    mov rdx, usage_msg_len
    syscall
    mov rdi, 2
    mov rax, sys_exit
    syscall
.have_dir:
    mov rax, sys_chdir
    mov rdi, [rsp + 16]             ; argv[1] = DIR
    syscall
    cmp rax, 0
    jl .error

    mov r13, -1
    mov rax, [rsp]
    cmp rax, 3
    jl .have_source
    mov rax, sys_open
    mov rdi, [rsp + 24]             ; argv[2] = source pipe
    mov rsi, O_RDONLY
    xor rdx, rdx
    syscall
    cmp rax, 0
    jl .error
    mov r13, rax
    cmp qword [rsp], 4              ; argv[3] present = wait mode
    jl .have_source
    mov qword [wait_mode], 1
.have_source:

    ; ignore SIGPIPE (writes to dead sockets return EPIPE) and SIGCHLD (auto-reap)
    mov rdi, SIGPIPE
    call ignore_signal
    mov rdi, SIGCHLD
    call ignore_signal

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
    lea r10, [so_reuseaddr]
    mov r8, so_reuseaddr_len
    syscall

    mov rax, sys_bind
    mov rdi, r12
    lea rsi, [sockaddr_in]
    mov rdx, sockaddr_in_len
    syscall
    cmp rax, 0
    jl .error

    mov rax, sys_listen
    mov rdi, r12
    mov rsi, 64
    syscall
    cmp rax, 0
    jl .error

    mov rax, sys_epoll_create1
    xor rdi, rdi
    syscall
    cmp rax, 0
    jl .error
    mov r14, rax

    mov rdi, r12
    mov rsi, TAG_LISTEN
    mov rdx, EPOLLIN
    call epoll_add
    cmp rax, 0
    jl .error

    cmp r13, -1
    je .no_source
    mov rdi, r13
    mov rsi, TAG_SOURCE
    xor rdx, rdx
    call epoll_add
    cmp rax, 0
    jl .error                       ; EPERM: the source is a regular file, not a pipe
    call set_source_interest
.no_source:

    mov rax, sys_write
    mov rdi, STDOUT_FILENO
    lea rsi, [listening_msg]
    mov rdx, listening_msg_len
    syscall

; ---------------------------------------------------------------------------
main:
.wait:
    mov rax, sys_epoll_wait
    mov rdi, r14
    lea rsi, [events]
    mov rdx, MAX_EVENTS
    mov r10, -1
    syscall
    cmp rax, 0
    jl .wait
    mov rbp, rax
    xor r15, r15
.each:
    cmp r15, rbp
    jge .wait
    lea rax, [r15 + r15*2]
    lea rdx, [events]
    lea rax, [rdx + rax*4]          ; events + r15*12
    mov ebx, dword [rax + 4]        ; fd
    mov ecx, dword [rax + 8]        ; tag
    inc r15
    cmp ecx, TAG_LISTEN
    je handle_listen
    cmp ecx, TAG_SOURCE
    je handle_source
    cmp ecx, TAG_PENDING
    je handle_pending
    jmp handle_sse

handle_listen:
    mov rax, sys_accept
    mov rdi, r12
    xor rsi, rsi
    xor rdx, rdx
    syscall
    cmp rax, 0
    jl main.each
    mov rdi, rax
    mov rsi, TAG_PENDING
    mov rdx, EPOLLIN
    call epoll_add
    jmp main.each

; ---------------------------------------------------------------------------
; A request arrived on a fresh connection.
; ---------------------------------------------------------------------------
handle_pending:
    mov rax, sys_read
    mov rdi, rbx
    lea rsi, [req_buf]
    mov rdx, REQ_BUF_SIZE
    syscall
    cmp rax, 0
    jg .have_req
    mov rdi, rbx
    call close_fd
    jmp main.each
.have_req:
    mov [req_len], rax
    call parse_request
    cmp rax, 0
    jl .bad
    mov rax, [method_id]
    cmp rax, METHOD_GET
    je .get
    cmp rax, METHOD_PUT
    je .put
    cmp rax, METHOD_DELETE
    je .delete
    lea rsi, [r405]
    mov rdx, r405_len
    lea r8, [s405]
    jmp .reply_close
.bad:
    lea rsi, [r400]
    mov rdx, r400_len
    lea r8, [s400]
.reply_close:
    push r8
    mov rdi, rbx
    call send_fd
    mov rdi, rbx
    call close_fd
    pop rsi
    mov rcx, 3
    mov rax, -1
    call log_request
    jmp main.each

.get:
    cmp qword [path_len], events_path_len
    jne .get_file
    lea rsi, [path_buf]
    lea rdi, [events_path]
    mov rcx, events_path_len
    repe cmpsb
    jne .get_file
    ; --- subscribe to the feed ---
    mov rdi, rbx
    lea rsi, [sse_hdr]
    mov rdx, sse_hdr_len
    call send_fd
    mov rdi, rbx
    call add_client
    cmp rax, 0
    jl .full
    mov rdi, rbx
    mov rsi, TAG_SSE
    mov rdx, EPOLLIN | EPOLLRDHUP
    call epoll_mod
    call set_source_interest
    jmp main.each
.full:
    mov rdi, rbx
    call close_fd
    jmp main.each
.get_file:
    call do_get
    jmp main.each
.put:
    call do_put
    jmp main.each
.delete:
    call do_delete
    jmp main.each

; a subscribed browser became readable: it hung up (or sent noise)
handle_sse:
    mov rax, sys_read
    mov rdi, rbx
    lea rsi, [in_buf]
    mov rdx, IN_BUF_SIZE
    syscall
    cmp rax, 0
    jg main.each
    mov rdi, rbx
    call remove_client
    mov rdi, rbx
    call close_fd
    call set_source_interest
    jmp main.each

; ---------------------------------------------------------------------------
; GET: file (forked sendfile), directory listing, or 301 to add the slash
; ---------------------------------------------------------------------------
do_get:
    mov rax, sys_open
    lea rdi, [path_buf]
    mov rsi, O_RDONLY
    xor rdx, rdx
    syscall
    cmp rax, 0
    jl .not_found
    mov [file_fd], rax
    mov rax, sys_fstat
    mov rdi, [file_fd]
    lea rsi, [stat_buf]
    syscall
    mov eax, dword [stat_buf + ST_MODE_OFF]
    and eax, S_IFMT
    cmp eax, S_IFDIR
    jne .file
    cmp qword [raw_ends_slash], 1
    je .listing

    ; --- 301: directory without trailing slash ---
    lea rdi, [out_buf]
    lea rsi, [r301_a]
    mov rcx, r301_a_len
    rep movsb
    mov rsi, [raw_path_ptr]
    mov rcx, [raw_path_len]
    rep movsb
    lea rsi, [r301_b]
    mov rcx, r301_b_len
    rep movsb
    lea rsi, [out_buf]
    mov rdx, rdi
    sub rdx, rsi
    mov rdi, rbx
    call send_fd
    mov rdi, [file_fd]
    call close_fd
    mov rdi, rbx
    call close_fd
    lea rsi, [s301]
    mov rcx, 3
    mov rax, -1
    jmp log_request                 ; tail call

.listing:
    call build_listing
    mov rdi, rbx
    lea rsi, [html_hdr]
    mov rdx, html_hdr_len
    call send_fd
    mov rdi, rbx
    lea rsi, [out_buf]
    mov rdx, [out_ptr]
    sub rdx, rsi
    call send_fd
    mov rdi, [file_fd]
    call close_fd
    mov rdi, rbx
    call close_fd
    lea rsi, [s200]
    mov rcx, 3
    mov rax, -1
    jmp log_request

.file:
    mov rax, [stat_buf + ST_SIZE_OFF]
    mov [file_size], rax
    lea rsi, [s200]                 ; log first: emit_log_line also uses out_buf
    mov rcx, 3
    call log_request
    lea rdi, [out_buf]
    lea rsi, [f200_a]
    mov rcx, f200_a_len
    rep movsb
    push rdi
    call mime_for_path              ; rsi, rcx
    pop rdi
    rep movsb
    lea rsi, [f200_b]
    mov rcx, f200_b_len
    rep movsb
    mov rax, [file_size]
    call append_dec
    lea rsi, [f200_c]
    mov rcx, f200_c_len
    rep movsb
    lea rax, [out_buf]
    sub rdi, rax
    mov [hdr_len], rdi
    mov rdi, rbx
    call epoll_del                  ; the child inherits the socket; keep epoll clean
    mov rax, sys_fork
    syscall
    test rax, rax
    jz get_child
    mov rdi, [file_fd]
    call close_fd
    mov rdi, rbx
    call close_fd
    ret

.not_found:
    mov rdi, rbx
    lea rsi, [r404]
    mov rdx, r404_len
    call send_fd
    mov rdi, rbx
    call close_fd
    lea rsi, [s404]
    mov rcx, 3
    mov rax, -1
    jmp log_request

get_child:                          ; send header, then the file, then exit
    mov rdi, rbx
    lea rsi, [out_buf]
    mov rdx, [hdr_len]
    call send_fd
.loop:
    mov r10, [file_size]
    test r10, r10
    jz .done
    cmp r10, SENDFILE_MAX
    jbe .chunk
    mov r10, SENDFILE_MAX
.chunk:
    mov rax, sys_sendfile
    mov rdi, rbx
    mov rsi, [file_fd]
    xor rdx, rdx
    syscall
    cmp rax, 0
    jle .fail
    sub [file_size], rax
    jmp .loop
.done:
    xor rdi, rdi
    mov rax, sys_exit_group
    syscall
.fail:
    mov rdi, 1
    mov rax, sys_exit_group
    syscall

; ---------------------------------------------------------------------------
; PUT: create/truncate the file, fork, child streams the body into it
; ---------------------------------------------------------------------------
do_put:
    cmp qword [raw_ends_slash], 1
    je .bad
    cmp qword [path_len], 1
    jne .path_ok
    cmp byte [path_buf], '.'
    je .bad
.path_ok:
    lea rsi, [hdr_content_length]
    mov rcx, hdr_content_length_len
    call find_header
    test rax, rax
    jz .need_length
    call parse_dec                  ; rax ptr, rdx len -> rax
    mov [body_len], rax
    call find_body_off
    mov [body_off], rax

    mov rax, sys_open
    lea rdi, [path_buf]
    mov rsi, O_WRONLY | O_CREAT | O_TRUNC
    mov rdx, 0o644
    syscall
    cmp rax, 0
    jl .cannot_open
    mov [file_fd], rax

    lea rsi, [hdr_expect]
    mov rcx, hdr_expect_len
    call find_header
    test rax, rax
    jz .no_expect
    mov rdi, rbx
    lea rsi, [r100]
    mov rdx, r100_len
    call send_fd
.no_expect:
    lea rsi, [s201]
    mov rcx, 3
    mov rax, [body_len]
    call log_request
    mov rdi, rbx
    call epoll_del
    mov rax, sys_fork
    syscall
    test rax, rax
    jz put_child
    mov rdi, [file_fd]
    call close_fd
    mov rdi, rbx
    call close_fd
    ret

.bad:
    lea rsi, [r400]
    mov rdx, r400_len
    lea r8, [s400]
    jmp .reply
.need_length:
    lea rsi, [r411]
    mov rdx, r411_len
    lea r8, [s411]
    jmp .reply
.cannot_open:
    lea rsi, [r500]
    mov rdx, r500_len
    lea r8, [s500]
.reply:
    push r8
    mov rdi, rbx
    call send_fd
    mov rdi, rbx
    call close_fd
    pop rsi
    mov rcx, 3
    mov rax, -1
    jmp log_request

put_child:
    ; body bytes that arrived with the headers
    lea rsi, [req_buf]
    add rsi, [body_off]
    mov rdx, [req_len]
    sub rdx, [body_off]
    cmp rdx, [body_len]
    jbe .first
    mov rdx, [body_len]
.first:
    test rdx, rdx
    jz .loop
    mov rax, sys_write
    mov rdi, [file_fd]
    syscall
    cmp rax, 0
    jl .fail
    sub [body_len], rax
.loop:
    mov rdx, [body_len]
    test rdx, rdx
    jz .done
    cmp rdx, XFER_BUF_SIZE
    jbe .rd
    mov rdx, XFER_BUF_SIZE
.rd:
    mov rax, sys_read
    mov rdi, rbx
    lea rsi, [xfer_buf]
    syscall
    cmp rax, 0
    jle .fail
    mov rdx, rax
    mov rax, sys_write
    mov rdi, [file_fd]
    lea rsi, [xfer_buf]
    syscall
    cmp rax, 0
    jl .fail
    sub [body_len], rax
    jmp .loop
.done:
    mov rdi, rbx
    lea rsi, [r201]
    mov rdx, r201_len
    call send_fd
    xor rdi, rdi
    mov rax, sys_exit_group
    syscall
.fail:
    mov rdi, 1
    mov rax, sys_exit_group
    syscall

; ---------------------------------------------------------------------------
; DELETE
; ---------------------------------------------------------------------------
do_delete:
    mov rax, sys_unlink
    lea rdi, [path_buf]
    syscall
    cmp rax, 0
    jl .nf
    lea rsi, [r204]
    mov rdx, r204_len
    lea r8, [s204]
    jmp .reply
.nf:
    lea rsi, [r404]
    mov rdx, r404_len
    lea r8, [s404]
.reply:
    push r8
    mov rdi, rbx
    call send_fd
    mov rdi, rbx
    call close_fd
    pop rsi
    mov rcx, 3
    mov rax, -1
    jmp log_request

; ---------------------------------------------------------------------------
; build_listing: directory page into out_buf; [file_fd] is the open dir.
; Leaves [out_ptr] = end of content.
; ---------------------------------------------------------------------------
build_listing:
    lea rdi, [out_buf]
    lea rsi, [page_head]
    mov rcx, page_head_len
    rep movsb
    lea rsi, [h2_a]
    mov rcx, h2_a_len
    rep movsb
    cmp qword [path_len], 1
    jne .show_path
    cmp byte [path_buf], '.'
    je .root
.show_path:
    lea rsi, [path_buf]
    mov rcx, [path_len]
    call append_html
.root:
    lea rsi, [h2_b]
    mov rcx, h2_b_len
    rep movsb
    cmp qword [path_len], 1
    jne .parent
    cmp byte [path_buf], '.'
    je .entries
.parent:
    lea rsi, [li_up]
    mov rcx, li_up_len
    rep movsb
.entries:
    mov [out_ptr], rdi
.more:
    mov rax, sys_getdents64
    mov rdi, [file_fd]
    lea rsi, [dents_buf]
    mov rdx, DENTS_BUF_SIZE
    syscall
    cmp rax, 0
    jle .finish
    lea rdx, [dents_buf]
    mov [dent_ptr], rdx
    add rdx, rax
    mov [dent_end], rdx
.entry:
    mov rsi, [dent_ptr]
    cmp rsi, [dent_end]
    jge .more
    movzx rax, word [rsi + 16]      ; d_reclen
    add rax, rsi
    mov [dent_ptr], rax             ; next entry
    lea r8, [rsi + 19]              ; d_name
    ; skip "." and ".."
    cmp byte [r8], '.'
    jne .named
    cmp byte [r8 + 1], 0
    je .entry
    cmp byte [r8 + 1], '.'
    jne .named
    cmp byte [r8 + 2], 0
    je .entry
.named:
    ; name length
    xor rcx, rcx
.len:
    cmp byte [r8 + rcx], 0
    je .have_len
    inc rcx
    jmp .len
.have_len:
    ; room check: worst case ~9x name + markup
    mov rdi, [out_ptr]
    lea rax, [out_buf + OUT_BUF_SIZE - 4096]
    cmp rdi, rax
    jae .truncate
    mov r9, rcx                     ; name len
    movzx r10, byte [rsi + 18]      ; d_type
    cmp r10, DT_DIR
    je .dir
    lea rsi, [li_file_a]
    mov rcx, li_file_a_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_urlenc
    lea rsi, [li_file_b]
    mov rcx, li_file_b_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_html
    lea rsi, [li_file_c]
    mov rcx, li_file_c_len
    rep movsb
    mov [out_ptr], rdi
    jmp .entry
.dir:
    lea rsi, [li_dir_a]
    mov rcx, li_dir_a_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_urlenc
    lea rsi, [li_dir_b]
    mov rcx, li_dir_b_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_html
    lea rsi, [li_dir_c]
    mov rcx, li_dir_c_len
    rep movsb
    mov [out_ptr], rdi
    jmp .entry
.truncate:
    lea rsi, [li_trunc]
    mov rcx, li_trunc_len
    rep movsb
    mov [out_ptr], rdi
.finish:
    mov rdi, [out_ptr]
    lea rsi, [ul_end]
    mov rcx, ul_end_len
    rep movsb
    lea rsi, [page_tail]
    mov rcx, page_tail_len
    rep movsb
    mov [out_ptr], rdi
    ret

; ---------------------------------------------------------------------------
; parse_request: method, raw path, decoded path (into path_buf). rax=0 ok/-1 bad
; ---------------------------------------------------------------------------
parse_request:
    lea rsi, [req_buf]
    mov rcx, [req_len]
    mov qword [method_id], 0
    cmp rcx, 8
    jl .bad
    cmp dword [rsi], 'GET '
    jne .not_get
    mov qword [method_id], METHOD_GET
    add rsi, 4
    sub rcx, 4
    jmp .path
.not_get:
    cmp dword [rsi], 'PUT '
    jne .not_put
    mov qword [method_id], METHOD_PUT
    add rsi, 4
    sub rcx, 4
    jmp .path
.not_put:
    cmp dword [rsi], 'DELE'
    jne .other
    cmp word [rsi + 4], 'TE'
    jne .other
    cmp byte [rsi + 6], ' '
    jne .other
    mov qword [method_id], METHOD_DELETE
    add rsi, 7
    sub rcx, 7
    jmp .path
.other:
    ; skip the unknown method token so the path still parses
.skipm:
    test rcx, rcx
    jz .bad
    mov al, [rsi]
    inc rsi
    dec rcx
    cmp al, ' '
    jne .skipm
.path:
    mov [raw_path_ptr], rsi
    xor rdx, rdx
.rawscan:
    cmp rdx, rcx
    jge .rawend
    mov al, [rsi + rdx]
    cmp al, ' '
    je .rawend
    cmp al, '?'
    je .rawend
    cmp al, 13
    je .rawend
    cmp al, 10
    je .rawend
    inc rdx
    jmp .rawscan
.rawend:
    mov [raw_path_len], rdx
    test rdx, rdx
    jz .bad
    cmp byte [rsi], '/'
    jne .bad
    mov qword [raw_ends_slash], 0
    cmp byte [rsi + rdx - 1], '/'
    jne .noslash
    mov qword [raw_ends_slash], 1
.noslash:
    inc rsi                         ; skip leading '/'
    dec rdx
    lea rdi, [path_buf]
    xor r8, r8
.dec:
    test rdx, rdx
    jz .decdone
    mov al, [rsi]
    inc rsi
    dec rdx
    cmp al, '%'
    jne .store
    cmp rdx, 2
    jl .bad
    mov al, [rsi]
    call hexval
    jc .bad
    shl al, 4
    mov r9b, al
    mov al, [rsi + 1]
    call hexval
    jc .bad
    or al, r9b
    add rsi, 2
    sub rdx, 2
.store:
    test al, al
    jz .bad
    cmp r8, PATH_BUF_SIZE
    jge .bad
    mov [rdi + r8], al
    inc r8
    jmp .dec
.decdone:
    mov byte [rdi + r8], 0
    mov [path_len], r8
    ; reject any ".." segment
    xor rcx, rcx
.seg:
    cmp rcx, r8
    jge .segs_ok
    cmp byte [rdi + rcx], '.'
    jne .adv
    cmp byte [rdi + rcx + 1], '.'
    jne .adv
    mov al, [rdi + rcx + 2]
    test al, al
    jz .bad
    cmp al, '/'
    je .bad
.adv:
    cmp rcx, r8
    jge .segs_ok
    mov al, [rdi + rcx]
    inc rcx
    cmp al, '/'
    jne .adv
    jmp .seg
.segs_ok:
    test r8, r8
    jnz .ok
    mov word [rdi], '.'             ; "." + NUL
    mov qword [path_len], 1
.ok:
    xor rax, rax
    ret
.bad:
    mov rax, -1
    ret

hexval:                             ; al = hex digit -> al = value, CF on error
    cmp al, '0'
    jb .no
    cmp al, '9'
    ja .alpha
    sub al, '0'
    clc
    ret
.alpha:
    or al, 0x20
    cmp al, 'a'
    jb .no
    cmp al, 'f'
    ja .no
    sub al, 'a' - 10
    clc
    ret
.no:
    stc
    ret

; find_header: rsi=name, rcx=len -> rax = value ptr (0 if absent), rdx = value len
find_header:
    mov r8, rsi
    mov r9, rcx
    lea rdi, [req_buf]
    mov r10, [req_len]
    add r10, rdi
.nextline:
    cmp rdi, r10
    jge .none
    cmp byte [rdi], 10
    je .lf
    inc rdi
    jmp .nextline
.lf:
    inc rdi
    cmp rdi, r10
    jge .none
    cmp byte [rdi], 13
    je .none                        ; blank line: end of headers
    cmp byte [rdi], 10
    je .none
    xor rcx, rcx
.cmp:
    cmp rcx, r9
    jge .name_ok
    lea rax, [rdi + rcx]
    cmp rax, r10
    jge .none
    mov al, [rdi + rcx]
    or al, 0x20
    mov dl, [r8 + rcx]
    or dl, 0x20
    cmp al, dl
    jne .nextline
    inc rcx
    jmp .cmp
.name_ok:
    lea rax, [rdi + r9]
    cmp byte [rax], ':'
    jne .nextline
    inc rax
.sp:
    cmp byte [rax], ' '
    jne .val
    inc rax
    jmp .sp
.val:
    mov rdx, rax
.vend:
    cmp rdx, r10
    jge .vdone
    cmp byte [rdx], 13
    je .vdone
    cmp byte [rdx], 10
    je .vdone
    inc rdx
    jmp .vend
.vdone:
    sub rdx, rax
    ret
.none:
    xor rax, rax
    ret

parse_dec:                          ; rax = ptr, rdx = len -> rax = value
    mov rsi, rax
    xor rax, rax
.loop:
    test rdx, rdx
    jz .done
    movzx rcx, byte [rsi]
    sub rcx, '0'
    cmp rcx, 9
    ja .done
    imul rax, rax, 10
    add rax, rcx
    inc rsi
    dec rdx
    jmp .loop
.done:
    ret

find_body_off:                      ; -> rax = offset of first body byte in req_buf
    lea rsi, [req_buf]
    mov rcx, [req_len]
    xor rax, rax
.scan:
    lea rdx, [rax + 4]
    cmp rdx, rcx
    jg .none
    cmp dword [rsi + rax], 0x0a0d0a0d   ; "\r\n\r\n"
    je .found
    inc rax
    jmp .scan
.found:
    add rax, 4
    ret
.none:
    mov rax, [req_len]
    ret

; mime_for_path -> rsi = mime string, rcx = len (from path_buf extension)
mime_for_path:
    lea rdi, [path_buf]
    mov rcx, [path_len]
    mov rax, -1
    xor rdx, rdx
.scan:
    cmp rdx, rcx
    jge .end
    mov r8b, [rdi + rdx]
    cmp r8b, '/'
    jne .nd
    mov rax, -1
    jmp .nx
.nd:
    cmp r8b, '.'
    jne .nx
    mov rax, rdx
.nx:
    inc rdx
    jmp .scan
.end:
    cmp rax, -1
    je .default
    inc rax
    mov r8, rcx
    sub r8, rax                     ; ext len
    test r8, r8
    jz .default
    cmp r8, 8
    jg .default
    mov qword [rsp - 16], 0
    xor rdx, rdx
    lea r11, [rdi + rax]            ; ext start
.cp:
    cmp rdx, r8
    jge .cmp
    mov r9b, [r11 + rdx]
    cmp r9b, 'A'
    jb .st
    cmp r9b, 'Z'
    ja .st
    or r9b, 0x20
.st:
    mov [rsp - 16 + rdx], r9b
    inc rdx
    jmp .cp
.cmp:
    mov r9, [rsp - 16]
    lea rdx, [mime_table]
.tl:
    cmp qword [rdx], 0
    je .default
    cmp [rdx], r9
    je .hit
    add rdx, 24
    jmp .tl
.hit:
    mov rsi, [rdx + 8]
    mov rcx, [rdx + 16]
    ret
.default:
    lea rsi, [mime_bin]
    mov rcx, mime_bin_len
    ret

; ---------------------------------------------------------------------------
; log_request: rsi = status str, rcx = len, rax = byte count or -1.
; Emits "METHOD /path STATUS (N bytes)" to the feed.
; ---------------------------------------------------------------------------
log_request:
    mov r8, rsi
    mov r9, rcx
    mov r10, rax
    lea rdi, [log_buf]
    mov rax, [method_id]
    cmp rax, METHOD_GET
    jne .np
    lea rsi, [m_get]
    mov rcx, 3
    jmp .m
.np:
    cmp rax, METHOD_PUT
    jne .nd
    lea rsi, [m_put]
    mov rcx, 3
    jmp .m
.nd:
    cmp rax, METHOD_DELETE
    jne .no
    lea rsi, [m_delete]
    mov rcx, 6
    jmp .m
.no:
    lea rsi, [m_other]
    mov rcx, 3
.m:
    rep movsb
    mov byte [rdi], ' '
    mov byte [rdi + 1], '/'
    add rdi, 2
    cmp qword [path_len], 1
    jne .path
    cmp byte [path_buf], '.'
    je .status
.path:
    lea rsi, [path_buf]
    mov rcx, [path_len]
    cmp rcx, 2000
    jbe .pc
    mov rcx, 2000
.pc:
    rep movsb
.status:
    mov byte [rdi], ' '
    inc rdi
    mov rsi, r8
    mov rcx, r9
    rep movsb
    cmp r10, -1
    je .emit
    mov word [rdi], ' ('
    add rdi, 2
    mov rax, r10
    call append_dec
    lea rsi, [log_bytes_b]
    mov rcx, log_bytes_b_len
    rep movsb
.emit:
    lea rsi, [log_buf]
    mov rcx, rdi
    sub rcx, rsi
    jmp emit_log_line

; ---------------------------------------------------------------------------
; source pipe readable
; ---------------------------------------------------------------------------
handle_source:
    mov rax, sys_read
    mov rdi, r13
    lea rsi, [in_buf]
    add rsi, [pending_len]
    mov rdx, IN_BUF_SIZE
    sub rdx, [pending_len]
    syscall
    cmp rax, 0
    jg .got_data
    mov rcx, [pending_len]
    test rcx, rcx
    jz .finish
    lea rsi, [in_buf]
    call emit_line
    mov qword [pending_len], 0
.finish:
    lea rsi, [done_msg]
    mov rdx, done_msg_len
    call broadcast
    mov rdi, r13
    call close_fd
    mov r13, -1
    mov rax, sys_write
    mov rdi, STDOUT_FILENO
    lea rsi, [source_done_msg]
    mov rdx, source_done_msg_len
    syscall
    jmp main.each
.got_data:
    add rax, [pending_len]
    mov [buf_total], rax
    mov qword [line_start], 0
    mov qword [scan_pos], 0
.scan:
    mov rax, [scan_pos]
    cmp rax, [buf_total]
    jge .scanned
    lea rsi, [in_buf]
    mov cl, [rsi + rax]
    cmp cl, 10
    je .eol
    cmp cl, 13
    je .eol
    inc qword [scan_pos]
    jmp .scan
.eol:
    mov rcx, rax
    sub rcx, [line_start]
    add rsi, [line_start]
    call emit_line
    mov rax, [scan_pos]
    inc rax
    mov [scan_pos], rax
    mov [line_start], rax
    jmp .scan
.scanned:
    mov rcx, [buf_total]
    sub rcx, [line_start]
    cmp rcx, IN_BUF_SIZE
    jl .keep_partial
    lea rsi, [in_buf]
    call emit_line
    xor rcx, rcx
    jmp .store_pending
.keep_partial:
    test rcx, rcx
    jz .store_pending
    lea rsi, [in_buf]
    add rsi, [line_start]
    lea rdi, [in_buf]
    push rcx
    rep movsb
    pop rcx
.store_pending:
    mov [pending_len], rcx
    call set_source_interest
    jmp main.each

; ---------------------------------------------------------------------------
; emit_line: rsi = line, rcx = len. "NN% " lines become progress signals,
; everything else a log line. Clobbers caller-saved registers.
; ---------------------------------------------------------------------------
emit_line:
    test rcx, rcx
    jz .ret
    xor rdx, rdx
.find_pct:
    cmp byte [rsi + rdx], '%'
    je .progress
    inc rdx
    cmp rdx, rcx
    jl .find_pct
    jmp emit_log_line
.progress:
    lea rax, [rdx + 1]              ; char after '%' must be space or end of line
    cmp rax, rcx
    jge .after_ok
    cmp byte [rsi + rax], ' '
    jne emit_log_line
.after_ok:
    mov rax, rdx
.back:
    test rax, rax
    jz .digits_found
    mov r8b, [rsi + rax - 1]
    cmp r8b, '0'
    jb .digits_found
    cmp r8b, '9'
    ja .digits_found
    dec rax
    jmp .back
.digits_found:
    cmp rax, rdx
    je emit_log_line
    jmp emit_progress_line
.ret:
    ret

emit_progress_line:                 ; rsi line, rcx len, rax digits start, rdx digits end
    mov r8, rsi
    mov r9, rcx
    mov r10, rax
    mov r11, rdx
    lea rdi, [out_buf]
    lea rsi, [sig_head]
    mov rcx, sig_head_len
    rep movsb
    lea rsi, [r8 + r10]
    mov rcx, r11
    sub rcx, r10
    rep movsb
    lea rsi, [sig_mid]
    mov rcx, sig_mid_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_json_safe
    lea rsi, [sig_tail]
    mov rcx, sig_tail_len
    rep movsb
    jmp broadcast_out

emit_log_line:                      ; rsi line, rcx len
    mov r8, rsi
    mov r9, rcx
    lea rdi, [out_buf]
    lea rsi, [el_head]
    mov rcx, el_head_len
    rep movsb
    mov rsi, r8
    mov rcx, r9
    call append_html
    lea rsi, [el_tail]
    mov rcx, el_tail_len
    rep movsb
    jmp broadcast_out

broadcast_out:
    lea rsi, [out_buf]
    mov rdx, rdi
    sub rdx, rsi
    jmp broadcast

; ---------------------------------------------------------------------------
; append_* helpers: rdi = destination (advanced on return)
; ---------------------------------------------------------------------------
append_json_safe:                   ; rsi, rcx: '"'->''' '\'->'/' ctrl->' '
    test rcx, rcx
    jz .done
.loop:
    lodsb
    cmp al, '"'
    jne .nq
    mov al, 39
.nq:
    cmp al, '\'
    jne .nb
    mov al, '/'
.nb:
    cmp al, 32
    jae .st
    mov al, ' '
.st:
    stosb
    dec rcx
    jnz .loop
.done:
    ret

append_html:                        ; rsi, rcx: escape < > & "
    test rcx, rcx
    jz .done
.loop:
    lodsb
    cmp al, '<'
    je .lt
    cmp al, '>'
    je .gt
    cmp al, '&'
    je .amp
    cmp al, '"'
    je .quot
    stosb
.next:
    dec rcx
    jnz .loop
.done:
    ret
.lt:
    mov dword [rdi], '&lt;'
    add rdi, 4
    jmp .next
.gt:
    mov dword [rdi], '&gt;'
    add rdi, 4
    jmp .next
.amp:
    mov dword [rdi], '&amp'
    mov byte [rdi + 4], ';'
    add rdi, 5
    jmp .next
.quot:
    mov dword [rdi], '&quo'
    mov word [rdi + 4], 't;'
    add rdi, 6
    jmp .next

append_urlenc:                      ; rsi, rcx: percent-encode all but A-Za-z0-9-._~
    test rcx, rcx
    jz .done
.loop:
    lodsb
    cmp al, '-'
    je .raw
    cmp al, '.'
    je .raw
    cmp al, '_'
    je .raw
    cmp al, '~'
    je .raw
    cmp al, '0'
    jb .enc
    cmp al, '9'
    jbe .raw
    cmp al, 'A'
    jb .enc
    cmp al, 'Z'
    jbe .raw
    cmp al, 'a'
    jb .enc
    cmp al, 'z'
    jbe .raw
.enc:
    mov byte [rdi], '%'
    mov dl, al
    shr dl, 4
    call hexchar
    mov [rdi + 1], dl
    mov dl, al
    and dl, 0x0f
    call hexchar
    mov [rdi + 2], dl
    add rdi, 3
    jmp .next
.raw:
    stosb
.next:
    dec rcx
    jnz .loop
.done:
    ret

hexchar:                            ; dl = 0..15 -> dl = '0'..'F'
    cmp dl, 10
    jb .d
    add dl, 'A' - 10
    ret
.d:
    add dl, '0'
    ret

append_dec:                         ; rax = value -> decimal text at rdi
    mov r8, 10
    xor rcx, rcx
    lea r9, [rsp - 8]               ; red zone scratch, digits grow downward
.div:
    xor rdx, rdx
    div r8
    add dl, '0'
    dec r9
    mov [r9], dl
    inc rcx
    test rax, rax
    jnz .div
    mov rsi, r9
    rep movsb
    ret

; ---------------------------------------------------------------------------
; broadcast: rsi = buf, rdx = len -> every client; dead ones are dropped
; ---------------------------------------------------------------------------
broadcast:
    push rbx
    xor rbx, rbx
.loop:
    cmp rbx, MAX_CLIENTS
    jge .done
    lea rax, [clients]
    mov rdi, [rax + rbx*8]
    test rdi, rdi
    jz .next
    call send_fd
    cmp rax, 0
    jge .next
    lea rax, [clients]
    mov rdi, [rax + rbx*8]
    mov qword [rax + rbx*8], 0
    dec qword [nclients]
    call close_fd
.next:
    inc rbx
    jmp .loop
.done:
    pop rbx
    ret

add_client:                         ; rdi = fd -> rax = 0 ok / -1 full
    lea rax, [clients]
    xor rcx, rcx
.scan:
    cmp qword [rax + rcx*8], 0
    je .found
    inc rcx
    cmp rcx, MAX_CLIENTS
    jl .scan
    mov rax, -1
    ret
.found:
    mov [rax + rcx*8], rdi
    inc qword [nclients]
    xor rax, rax
    ret

remove_client:                      ; rdi = fd
    lea rax, [clients]
    xor rcx, rcx
.scan:
    cmp [rax + rcx*8], rdi
    je .found
    inc rcx
    cmp rcx, MAX_CLIENTS
    jl .scan
    ret
.found:
    mov qword [rax + rcx*8], 0
    dec qword [nclients]
    ret

; Default: always drain the source. Wait mode: read only while someone watches.
set_source_interest:
    cmp r13, -1
    je .ret
    mov rdx, EPOLLIN
    cmp qword [wait_mode], 0
    je .apply
    cmp qword [nclients], 0
    jne .apply
    xor rdx, rdx
.apply:
    mov rdi, r13
    mov rsi, TAG_SOURCE
    call epoll_mod
.ret:
    ret

; ---------------------------------------------------------------------------
; syscall helpers
; ---------------------------------------------------------------------------
epoll_add:                          ; rdi = fd, rsi = tag, rdx = events
    mov r8, EPOLL_CTL_ADD
    jmp epoll_ctl_do
epoll_mod:
    mov r8, EPOLL_CTL_MOD
epoll_ctl_do:
    mov dword [ev], edx
    mov dword [ev + 4], edi
    mov dword [ev + 8], esi
    mov rdx, rdi
    mov rdi, r14
    mov rsi, r8
    lea r10, [ev]
    mov rax, sys_epoll_ctl
    syscall
    ret

epoll_del:                          ; rdi = fd
    mov rdx, rdi
    mov rdi, r14
    mov rsi, EPOLL_CTL_DEL
    lea r10, [ev]
    mov rax, sys_epoll_ctl
    syscall
    ret

send_fd:                            ; rdi = fd, rsi = buf, rdx = len (rsi/rdx preserved)
    mov rax, sys_sendto
    mov r10, MSG_NOSIGNAL
    xor r8, r8
    xor r9, r9
    syscall
    ret

close_fd:                           ; rdi = fd
    mov rax, sys_close
    syscall
    ret

ignore_signal:                      ; rdi = signal number
    lea rsi, [sigign_act]
    xor rdx, rdx
    mov r10, 8
    mov rax, sys_rt_sigaction
    syscall
    ret

_start.error:
    neg rax
    mov rdi, rax
    mov rax, sys_exit
    syscall
