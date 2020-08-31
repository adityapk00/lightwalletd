我是光年实验室高级招聘经理。
我在github上访问了你的开源项目，你的代码超赞。你最近有没有在看工作机会，我们在招软件开发工程师，拉钩和BOSS等招聘网站也发布了相关岗位，有公司和职位的详细信息。
我们公司在杭州，业务主要做流量增长，是很多大型互联网公司的流量顾问。公司弹性工作制，福利齐全，发展潜力大，良好的办公环境和学习氛围。
公司官网是http://www.gnlab.com,公司地址是杭州市西湖区古墩路紫金广场B座，若你感兴趣，欢迎与我联系，
电话是0571-88839161，手机号：18668131388，微信号：echo 'bGhsaGxoMTEyNAo='|base64 -D ,静待佳音。如有打扰，还请见谅，祝生活愉快工作顺利。

# Overview

Zecwallet lightwalletd is a fork of [lightwalletd](https://github.com/adityapk00/lightwalletd) from the ECC. 

It is a backend service that provides a bandwidth-efficient interface to the Zcash blockchain for the [Zecwallet light wallet](https://github.com/adityapk00/zecwallet-lite-lib).

## Changes from upstream lightwalletd
This version of Zecwallet lightwalletd extends lightwalletd and:
* Adds support for transparent addresses
* Adds several new RPC calls for lightclients
* Lots of perf improvements
  * Replaces SQLite with in-memory cache for Compact Blocks
  * Replace local Txstore, delegating Tx lookups to Zcashd
  * Remove the need for a separate ingestor

## Running your own zeclite lightwalletd

#### 0. First, install [Go >= 1.11](https://golang.org/dl/#stable).

#### 1. Run a zcash node.
Start a `zcashd` with the following options:
```
server=1
rpcuser=user
rpcpassword=password
rpcbind=127.0.0.1
rpcport=8232
experimentalfeatures=1
txindex=1
insightexplorer=1
```

You might need to run with `-reindex` the first time if you are enabling the `txindex` or `insightexplorer` options for the first time. The reindex might take a while. If you are using it on testnet, please also include `testnet=1`

#### 2. Get a TLS certificate

##### a. "Let's Encrypt" certificate using NGINX as a reverse proxy
If you running a public-facing server, the easiest way to obtain a certificate is to use a NGINX reverse proxy and get a Let's Encrypt certificate. [Instructions are here](https://www.nginx.com/blog/using-free-ssltls-certificates-from-lets-encrypt-with-nginx/)

Create a new section for the NGINX reverse proxy:
```
server {
    listen 443 ssl http2;
 
 
    ssl_certificate     ssl/cert.pem; # From certbot
    ssl_certificate_key ssl/key.pem;  # From certbot
    
    location / {
        # Replace localhost:9067 with the address and port of your gRPC server if using a custom port
        grpc_pass grpc://localhost:9067;
    }
}
```

##### b. Use without TLS certificate
You can run lightwalletd without TLS and server traffic over `http`. This is recommended only for local testing

#### 3. Run the frontend:
You can run the gRPC server with or without TLS, depending on how you configured step 2. If you are using NGINX as a reverse proxy and are letting NGINX handle the TLS authentication, then run the frontend with `-no-tls`

```
go run cmd/server/main.go -bind-addr 127.0.0.1:9067 -conf-file ~/.zcash/zcash.conf -no-tls
```

If you have a certificate that you want to use (either self signed, or from a certificate authority), pass the certificate to the frontend:

```
go run cmd/server/main.go -bind-addr 127.0.0.1:443 -conf-file ~/.zcash/zcash.conf  -tls-cert cert.pem -tls-key key.pem
```

You should start seeing the frontend ingest and cache the zcash blocks after ~15 seconds. 

#### 4. Point the `zecwallet-cli` to this server
Connect to your server!
```
./zecwallet-cli -server https://mylightwalletd.server.com:443
```

If you are using your own server running without TLS, you can also connect over `http`

```
./zecwallet-cli --server http://127.0.0.1:9067
```
