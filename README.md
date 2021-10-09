# Webhook daemon

[![Build and release](https://github.com/reddec/wd/actions/workflows/release.yaml/badge.svg)](https://github.com/reddec/wd/actions/workflows/release.yaml)

Yet another application which can run scripts on request.

Supports:

* Any executable script in directory can be run
* Isolated temporary working directory for scripts for each run (disabled by `-I`)
* Buffers response to handle proper response in case scrip non-zero exit code
* Exports Prometheus metrics (available on `/metrics`, disabled by `-M`)
* Supports TLS and automatic TLS by Let's encrypt (`--auto-tls example.com`)
* Supports JWT tokens (`-s secret2 ...`, issue by token `token`)
* Supports two mode:
    * scripts from directory (ex: `wd serve path/to/dir`)
    * single script from command line (ex: `wd run date`)
* Designed to work in memory-constrained environments
* Supports async mode


## Installation

### Precompiled binaries

From binaries and packages - [releases page](https://github.com/reddec/wd/releases)

**Debian/Ubuntu** packages defaults:
* systemd service `webhooks`, enabled and started after installation
* webhooks directory is `/var/webhooks`
* `run-as-script-owner` enabled by default
* metrics are disabled
* configuration file is in `/etc/webhooks/webhooks.env` with 0600 permission
* binding is `127.0.0.1:8080`

### Brew (MacOS)

    brew install reddec/tap/wd

Supports both Intel and Apple Silicon (arm)

### Build

From source, requires g1.17+
    
    go install github.com/reddec/wd@latest

## Minimal running examples

show current date

    wd run date

expose current dir

    wd serve .

## Usage

Even if `wd` comes with already good enough default parameters, it is not opinionated and allows 
precisely adjust almost any kind of behaviour.

### Metrics

By-default, `wd` exposes metrics endpoint as `/metrics` without restrictions. You may:

* disable metrics completely by `M, --disable-metrics`
* put metrics endpoint behind tokens (requires `-s ...`) by `--secure-metrics`. Tokens should be issued for `metrics`
  action.

### Async execution

In case of asynchronous execution:

1. request will be dumped to a temporary file on disk
2. client will get 204 No Content
3. in background go-routine, request will be streamed from disk like it was sent by client
4. file name will be added to the internal queue (`-q, --queue`)
5. it will retry execute request again and again during 1 + `--retries` attempts in case non-2xx code returned. Output
   will be dropped.

Maximum number of parallel async worker can be limited by `-A,--async-worker`, default is `2`.

Async mode can be activated by:

* (default) in `--async auto` mode - by query parameter `async=(y,yes,true,t,on,ok,1)`
* forcefully for all requests by `--async forced`

to disable async execution completely use flag `--async disabled`

During async execution the special env variable `HEADER_X_ATTEMPT` will be passed to the script. It contains attempt
number starting from 1.

### Payload

By-default, request body will be streamed to STDIN of script. This approach allows users to minimize memory consumption
because no information store in memory, so it's possible to handle unlimited amount of data (ie: streaming).

However, in case script does not support processing of STDIN (or just for convenience) it is possible to cache payload
into memory and pass it as environment variable `REQUEST_PAYLOAD` (`--payload env`) or as last argument of
a script (`--payload arg`). It might be a good idea to adjust maximum payload size by
`--payload-size, -P` (default is 10Mib).

> Do not use `--payload env` or `--payload arg` for large payloads!

General linux limits: 
* Max argument size - ~32Kib and could vary for each setup
* Max environment variable size - at least 4Kib and could vary for each setup

In case of `--payload env` or `--payload  arg` payload has to be read fully before passed to a script
which requires additional memory close double of request body size.

Shorthand for `--payload` flag is `-p`.

### Common

```
Usage:
  wd [OPTIONS] <run | serve | token>

Application Options:
      --cors                         Enable CORS [$CORS]
  -b, --bind=                        Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=                     Maximum execution timeout (default: 120s) [$TIMEOUT]
  -s, --secret=                      JWT secret for checking tokens. Use token command to create token [$SECRET]
  -B, --buffer=                      Buffer response size (default: 8192) [$BUFFER]
  -a, --async=[auto|forced|disabled] Async mode. auto - relies on async param in query, forced - always async, disabled - no async (default: auto) [$ASYNC]
  -r, --retries=                     Number of additional retries after first attempt (async only) (default: 3) [$RETRIES]
  -d, --delay=                       Delay between attempts (async only) (default: 3s) [$DELAY]
  -W, --workers=                     Maximum number of workers for sync requests. Default is 2 x num CPU [$WORKERS]
  -A, --async-workers=               Number of workers to process async requests (default: 2) [$ASYNC_WORKERS]
  -q, --queue=                       Queue size for async requests. 0 means unbound (default: 8192) [$QUEUE]
  -p, --payload=[stdin|arg|env]      Payload type - how to pass request body to the script (default: stdin) [$PAYLOAD]
  -M, --disable-metrics              Disable prometheus metrics [$DISABLE_METRICS]
      --secure-metrics               Require token to access metrics endpoint [$SECURE_METRICS]
      --auto-tls=                    Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir=          Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                          Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=                    Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=                     Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                         Show this help message

Available commands:
  run    run single script
  serve  serve server from directory
  token  issue token
```

### Run

Run single script. Uses current work dir as work dir for script.

```
Usage:
  wd [OPTIONS] run [Binary] [Args...]

Application Options:
      --cors                         Enable CORS [$CORS]
  -b, --bind=                        Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=                     Maximum execution timeout (default: 120s) [$TIMEOUT]
  -s, --secret=                      JWT secret for checking tokens. Use token command to create token [$SECRET]
  -B, --buffer=                      Buffer response size (default: 8192) [$BUFFER]
  -a, --async=[auto|forced|disabled] Async mode. auto - relies on async param in query, forced - always async, disabled - no async (default: auto) [$ASYNC]
  -r, --retries=                     Number of additional retries after first attempt (async only) (default: 3) [$RETRIES]
  -d, --delay=                       Delay between attempts (async only) (default: 3s) [$DELAY]
  -W, --workers=                     Maximum number of workers for sync requests. Default is 2 x num CPU [$WORKERS]
  -A, --async-workers=               Number of workers to process async requests (default: 2) [$ASYNC_WORKERS]
  -q, --queue=                       Queue size for async requests. 0 means unbound (default: 8192) [$QUEUE]
  -p, --payload=[stdin|arg|env]      Payload type - how to pass request body to the script (default: stdin) [$PAYLOAD]
  -P, --payload-size=                Maximum payload size in bytes. Zero or negative means unlimited (default: 10485760) [$PAYLOAD_SIZE]
  -M, --disable-metrics              Disable prometheus metrics [$DISABLE_METRICS]
      --secure-metrics               Require token to access metrics endpoint [$SECURE_METRICS]
      --auto-tls=                    Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir=          Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                          Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=                    Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=                     Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                         Show this help message

[run command arguments]
  Binary:                            binary to run
  Args:                              arguments
```

Examples:

**current date**

`wd run date`

**current date in seconds**

`wd run -- date +%s`

### Serve

Map request path to script inside directory. It's forbidden to execute scripts outside directory (parents). By-default,
directory and scripts with leading .dot disabled.

To be more secure, you may run `wd` as root and add flag `-R, --run-as-script-owner` (works only on posix). In that case
`wd` will run script with same uid/gid as in file. Basically, if you want to run script as specific user - just
do `chown` on it. If isolation not disabled, temporary work directory also will be chown to the script uid/gid.

```
Usage:
  wd [OPTIONS] serve [serve-OPTIONS] [Scripts]

Application Options:
      --cors                         Enable CORS [$CORS]
  -b, --bind=                        Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=                     Maximum execution timeout (default: 120s) [$TIMEOUT]
  -s, --secret=                      JWT secret for checking tokens. Use token command to create token [$SECRET]
  -B, --buffer=                      Buffer response size (default: 8192) [$BUFFER]
  -a, --async=[auto|forced|disabled] Async mode. auto - relies on async param in query, forced - always async, disabled - no async (default: auto) [$ASYNC]
  -r, --retries=                     Number of additional retries after first attempt (async only) (default: 3) [$RETRIES]
  -d, --delay=                       Delay between attempts (async only) (default: 3s) [$DELAY]
  -W, --workers=                     Maximum number of workers for sync requests. Default is 2 x num CPU [$WORKERS]
  -A, --async-workers=               Number of workers to process async requests (default: 2) [$ASYNC_WORKERS]
  -q, --queue=                       Queue size for async requests. 0 means unbound (default: 8192) [$QUEUE]
  -p, --payload=[stdin|arg|env]      Payload type - how to pass request body to the script (default: stdin) [$PAYLOAD]
  -P, --payload-size=                Maximum payload size in bytes. Zero or negative means unlimited (default: 10485760) [$PAYLOAD_SIZE]
  -M, --disable-metrics              Disable prometheus metrics [$DISABLE_METRICS]
      --secure-metrics               Require token to access metrics endpoint [$SECURE_METRICS]
      --auto-tls=                    Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir=          Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                          Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=                    Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=                     Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                         Show this help message

[serve command options]
      -R, --run-as-script-owner      Run scripts from the same Gid/Uid as file. If isolation enabled, temp dir will be also chown. Requires root [$RUN_AS_SCRIPT_OWNER]
      -w, --work-dir=                Working directory [$WORK_DIR]
      -I, --disable-isolation        Disable isolated work dirs [$DISABLE_ISOLATION]
      -D, --enable-dot-files         Enable lookup for scripts in dor directories and files [$ENABLE_DOT_FILES]

[serve command arguments]
  Scripts:                           Scripts directory
```

Example:

**expose scripts in current dir**

```
wd serve .
```

in case there is a script `echo.sh` in the current directory, it will be available over `/echo.sh`.

### Token

Issue JWT token. By default - there is no expiration time and there is no limits for hooks.

```
Usage:
  wd [OPTIONS] token [token-OPTIONS] [Hooks...]

Application Options:
      --cors                         Enable CORS [$CORS]
  -b, --bind=                        Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=                     Maximum execution timeout (default: 120s) [$TIMEOUT]
  -s, --secret=                      JWT secret for checking tokens. Use token command to create token [$SECRET]
  -B, --buffer=                      Buffer response size (default: 8192) [$BUFFER]
  -a, --async=[auto|forced|disabled] Async mode. auto - relies on async param in query, forced - always async, disabled - no async (default: auto) [$ASYNC]
  -r, --retries=                     Number of additional retries after first attempt (async only) (default: 3) [$RETRIES]
  -d, --delay=                       Delay between attempts (async only) (default: 3s) [$DELAY]
  -W, --workers=                     Maximum number of workers for sync requests. Default is 2 x num CPU [$WORKERS]
  -A, --async-workers=               Number of workers to process async requests (default: 2) [$ASYNC_WORKERS]
  -q, --queue=                       Queue size for async requests. 0 means unbound (default: 8192) [$QUEUE]
  -p, --payload=[stdin|arg|env]      Payload type - how to pass request body to the script (default: stdin) [$PAYLOAD]
  -M, --disable-metrics              Disable prometheus metrics [$DISABLE_METRICS]
      --secure-metrics               Require token to access metrics endpoint [$SECURE_METRICS]
      --auto-tls=                    Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir=          Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                          Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=                    Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=                     Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                         Show this help message

[token command options]
      -n, --name=                    Name of token, will be mapped as sub [$NAME]
      -e, --expiration=              Token expiration. Zero means no expiration (default: 0) [$EXPIRATION]

[token command arguments]
  Hooks:                             allowed hooks (nothing means all hooks)
```

**basic token**

    wd -s secret1 token

All hooks are allowed. Response can be used as content of `Authorization` header or query parameter `token`.

**named token**

    wd -s secret1 token -n token-name

**token with expiration**

    wd -s secret1 token -e 12h

**token restricted to specific hooks**

    wd -s secret1 token hook1 hook2 hook3
   
