# Overview

Safewallet lightwalletd is a fork of [lightwalletd](https://github.com/OleksandrBlack/safecoin-lightwalletd) from the ECC. 

It is a backend service that provides a bandwidth-efficient interface to the Safecoin blockchain for the [Safewallet light wallet](https://github.com/OleksandrBlack/safewallet-light-cli).

## Changes from upstream lightwalletd
This version of Safewallet lightwalletd extends lightwalletd and:
* Adds support for transparent addresses
* Adds several new RPC calls for lightclients
* Lots of perf improvements
  * Replaces SQLite with in-memory cache for Compact Blocks
  * Replace local Txstore, delegating Tx lookups to Safecoind
  * Remove the need for a separate ingestor

## Running your own safelite lightwalletd

#### 0. First, install [Go >= 1.12](https://golang.org/dl/#stable).

#### 1. Generate a TLS self-signed certificate
```
openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes
```
Answer the certificate questions to generate the self-signed certificate

#### 2. You need to run a safecoin full node with the following options in safecoin.conf
```
server=1
rpcuser=user
rpcpassword=password
rpcbind=127.0.0.1
rpcport=8771
txindex=1
```

#### 3. Run the frontend:
You'll need to use the certificate generated from step 1
```
go run cmd/server/main.go -bind-addr 127.0.0.1:9071 -conf-file ~/.safecoin/safecoin.conf  -tls-cert cert.pem -tls-key key.pem
```

#### 4. Point the `zecwallet-cli` to this server
```
./zecwallet-cli --server https://127.0.0.1:9071 --dangerous
```
