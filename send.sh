#!/bin/bash
# sending everything
# scp -r /Users/ZNM4U7N/projects/other/pi-pn532-i2c-Ntag21x-ws/* reeceappling@192.168.1.159:/home/reeceappling/rfidTest/
# sending only this script
# scp /Users/ZNM4U7N/projects/other/pi-pn532-i2c-Ntag21x-ws/send.sh reeceappling@192.168.1.159:/home/reeceappling/rfidTest/
# TODO: DO THIS ON THE PI INSTEAD!
# 1. Create a 128MB RAM disk for Go's temporary compiler files
mkdir -p /home/reeceappling/gotmp
sudo mount -t tmpfs -o size=256M tmpfs /home/reeceappling/gotmp
#sudo mount -o remount,size=256M /home/pi/gotmp
# Tell Go to use the RAM disk. Install for incremental dependency management, and then build with CGO flags
export GOTMPDIR=/home/reeceappling/gotmp
go install -v ./...
export CGO_CFLAGS="-O0 -pipe"
export CGO_LDFLAGS="-O0"
go build -o startRfid.goexe -p 4 -v .
#CGO_CFLAGS="-O0 -pipe" CGO_LDFLAGS="-O0" go build -o startRfid.goexe -p 4 -v .
sudo umount /home/reeceappling/gotmp
unset GOTMPDIR
unset CGO_CFLAGS
unset CGO_LDFLAGS

#env GOOS=linux GOARCH=arm GOARM=7 go build -o startRfid.goexe .
#chmod 777 startRfid
#scp startRfid reeceappling@192.168.1.159:/home/reeceappling/
# go build -o startRfid.goexe -v .