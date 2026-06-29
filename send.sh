#!/bin/bash
env GOOS=linux GOARCH=arm GOARM=7 go build -o startRfid.goexe main.go
chmod 777 startRfid
scp startRfid reeceappling@192.168.1.159:/home/reeceappling/