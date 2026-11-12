setup:
    qemu-img create -f qcow2 testfiles/windows-core-2016.qcow2 20G
    qemu-system-x86_64 \
        -m 2048 \
        -smp 2 \
        -cdrom ~/Downloads/server-2016.iso \
        -drive file=testfiles/windows-core-2016.qcow2,format=qcow2 \
        -boot d

vm:
    qemu-system-x86_64 \
        -machine q35 \
        -m 10100 \
        -smp 6 \
        -drive file=testfiles/windows-core-2016.qcow2,format=qcow2 \
        -net nic -net user,hostfwd=tcp::5985-:5985,hostfwd=tcp::5986-:5986

build:
    go build ./cmd/good-winrm

release:
    go build -ldflags="-s -w" ./cmd/good-winrm
    GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o good-winrm.windows-x86_64.exe ./cmd/good-winrm
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o good-winrm.linux-x86_64 ./cmd/good-winrm
