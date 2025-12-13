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
        -m 8100 \
        -display none \
        -smp 6 \
        -drive file=testfiles/windows-core-2016.qcow2,format=qcow2 \
        -net nic -net user,hostfwd=tcp::5985-:5985,hostfwd=tcp::5986-:5986

build:
    go build ./cmd/good-winrm

release:
    go build -ldflags="-s -w" ./cmd/good-winrm