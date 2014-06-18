#!/bin/bash

# Copyright (C) 2014 Jakob Borg and other contributors. All rights reserved.
# Use of this source code is governed by an MIT-style license that can be
# found in the LICENSE file.

iterations=${1:-5}

id1=I6KAH7666SLLL5PFXSOAUFJCDZYAOMLEKCP2GB3BV5RQST3PSROA
id2=JMFJCXBGZDE4BOCJE3VF65GYZNAIVJRET3J6HMRAUQIGJOFKNHMQ
id3=373HSRPQLPNLIJYKZVQFP4PKZ6R2ZE6K3YD442UJHBGBQGWWXAHA

go build genfiles.go
go build md5r.go
go build json.go

start() {
	echo "Starting..."
	for i in 1 2 3 4 ; do
		STPROFILER=":909$i" syncthing -home "h$i" > "$i.out" 2>&1 &
	done
}

clean() {
	if [[ $(uname -s) == "Linux" ]] ; then
		grep -v utf8-nfd
	else
		cat
	fi
}


testConvergence() {
	while true ; do
		sleep 5
		s1comp=$(curl -HX-API-Key:abc123 -s "http://localhost:8082/rest/connections" | ./json "$id1/Completion")
		s2comp=$(curl -HX-API-Key:abc123 -s "http://localhost:8083/rest/connections" | ./json "$id2/Completion")
		s3comp=$(curl -HX-API-Key:abc123 -s "http://localhost:8081/rest/connections" | ./json "$id3/Completion")
		s1comp=${s1comp:-0}
		s2comp=${s2comp:-0}
		s3comp=${s3comp:-0}
		tot=$(($s1comp + $s2comp + $s3comp))
		echo $tot / 300
		if [[ $tot == 300 ]] ; then
			break
		fi
	done

	echo "Verifying..."
	cat md5-? | sort | clean | uniq > md5-tot
	cat md5-12-? | sort | clean | uniq > md5-12-tot
	cat md5-23-? | sort | clean | uniq > md5-23-tot

	for i in 1 2 3 12-1 12-2 23-2 23-3; do
		pushd "s$i" >/dev/null
		../md5r -l | sort | clean > ../md5-$i
		popd >/dev/null
	done

	ok=0
	for i in 1 2 3 ; do
		if ! cmp "md5-$i" md5-tot >/dev/null ; then
			echo "Fail: instance $i unconverged for default"
		else
			ok=$(($ok + 1))
			echo "OK: instance $i converged for default"
		fi
	done
	for i in 12-1 12-2 ; do
		if ! cmp "md5-$i" md5-12-tot >/dev/null ; then
			echo "Fail: instance $i unconverged for s12"
		else
			ok=$(($ok + 1))
			echo "OK: instance $i converged for s12"
		fi
	done
	for i in 23-2 23-3 ; do
		if ! cmp "md5-$i" md5-23-tot >/dev/null ; then
			echo "Fail: instance $i unconverged for s23"
		else
			ok=$(($ok + 1))
			echo "OK: instance $i converged for s23"
		fi
	done
	if [[ $ok != 7 ]] ; then
		pkill syncthing
		exit 1
	fi
}

alterFiles() {
	pkill -STOP syncthing
	for i in 1 2 3 12-1 12-2 23-2 23-3 ; do
		pushd "s$i" >/dev/null

		nfiles=$(find . -type f | wc -l)
		if [[ $nfiles > 2000 ]] ; then
			todelete=$(( $nfiles - 2000 ))
			echo "Deleting $todelete files..."
			find . -type f \
				| grep -v large \
				| sort -k 1.16 \
				| head -n "$todelete" \
				| xargs rm -f
		fi

		../genfiles -maxexp 22 -files 600
		echo "  $i: append to large file"
		dd if=/dev/urandom bs=1024k count=4 >> large-$i 2>/dev/null
		../md5r -l > ../md5-tmp
		(grep -v large ../md5-tmp ; grep "large-$i" ../md5-tmp) | grep -v '/.syncthing.' > ../md5-$i
		popd >/dev/null
	done
	pkill -CONT syncthing
}

rm -f h?/*.idx.gz h?/files.db
rm -rf s? s??-? s4d

echo "Setting up files..."
for i in 1 2 3 12-1 12-2 23-2 23-3; do
	mkdir "s$i"
	pushd "s$i" >/dev/null
	echo "  $i: random nonoverlapping"
	../genfiles -maxexp 22 -files 600
	echo "  $i: empty file"
	touch "empty-$i"
	echo "  $i: large file"
	dd if=/dev/urandom of=large-$i bs=1024k count=55 2>/dev/null
	echo "  $i: weird encodings"
	echo somedata > "$(echo -e utf8-nfc-\\xc3\\xad)-$i"
	echo somedata > "$(echo -e utf8-nfd-i\\xcc\\x81)-$i"
	echo somedata > "$(echo -e cp850-\\xa1)-$i"
	touch "empty-$i"
	popd >/dev/null
done

mkdir s4d
echo somerandomdata > s4d/extrafile

echo "MD5-summing..."
for i in 1 2 3 12-1 12-2 23-2 23-3 ; do
	pushd "s$i" >/dev/null
	../md5r -l > ../md5-$i
	popd >/dev/null
done

start
testConvergence

for ((t = 1; t <= $iterations; t++)) ; do
	echo "Add and remove random files ($t / $iterations)..."
	alterFiles

	echo "Waiting..."
	sleep 30
	testConvergence
done

for i in 1 2 3 4 ; do
	curl -HX-API-Key:abc123 -X POST "http://localhost:808$i/rest/shutdown"
done
