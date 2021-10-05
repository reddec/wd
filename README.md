# Webhook daemon

Yet another application which can run scripts on request.

Supports:

* Any executable script in directory can be run
* Isolated temporary working directory for scripts for each run (disabled by `-I`)
* Buffers response to handle proper response in case scrip non-zero exit code
* Exports Prometheus metrics (available on `/metrics`, disabled by `-M`)
* Supports TLS and automatic TLS by Let's encrypt (`--auto-tls example.com`)
* Supports basic authorization by tokens (`-T secret1 -T secret2 ...`)
* Supports two mode: 
  * scripts from directory (ex: `wd serve path/to/dir`)
  * single script from command line (ex: `wd run date`)

## Usage

### Common

```
Usage:
  wd [OPTIONS] <run | serve>

Application Options:
  -b, --bind=               Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=            Maximum execution timeout (default: 120s) [$TIMEOUT]
  -T, --tokens=             Basic authorization (if at least one defined) by Authorization content or token in query [$TOKENS]
  -B, --buffer=             Buffer response size (default: 8192) [$BUFFER]
  -M, --disable-metrics     Disable prometheus metrics [$DISABLE_METRICS]
      --auto-tls=           Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir= Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                 Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=           Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=            Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                Show this help message

Available commands:
  run    run single script
  serve  serve server from directory

```

### Run 

Run single script. Uses current work dir as work dir for script.

```
Usage:
  wd [OPTIONS] run [Binary] [Args...]

Application Options:
  -b, --bind=               Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=            Maximum execution timeout (default: 120s) [$TIMEOUT]
  -T, --tokens=             Basic authorization (if at least one defined) by Authorization content or token in query [$TOKENS]
  -B, --buffer=             Buffer response size (default: 8192) [$BUFFER]
  -M, --disable-metrics     Disable prometheus metrics [$DISABLE_METRICS]
      --auto-tls=           Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls [$AUTO_TLS]
      --auto-tls-cache-dir= Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                 Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=           Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=            Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                Show this help message

[run command arguments]
  Binary:                   binary to run
  Args:                     arguments

```

Examples:

**current date**

`wd run date`

**current date in seconds**

`wd run -- date +%s`



### Serve

Map request path to script inside directory. It's forbidden to execute scripts outside directory (parents).
By-default, directory and scripts with leading .dot disabled. 

```
Usage:
  wd [OPTIONS] serve [serve-OPTIONS] [Scripts]

Application Options:
  -b, --bind=                  Binding address (default: 127.0.0.1:8080) [$BIND]
  -t, --timeout=               Maximum execution timeout (default: 120s) [$TIMEOUT]
  -T, --tokens=                Basic authorization (if at least one defined) by Authorization content or token in query [$TOKENS]
  -B, --buffer=                Buffer response size (default: 8192) [$BUFFER]
  -M, --disable-metrics        Disable prometheus metrics [$DISABLE_METRICS]
      --auto-tls=              Automatic TLS (Let's Encrypt) for specified domains. Service must be accessible by 80/443 port. Disables --tls
                               [$AUTO_TLS]
      --auto-tls-cache-dir=    Location where to store certificates (default: .certs) [$AUTO_TLS_CACHE_DIR]
      --tls                    Enable HTTPS serving with TLS. Ignored with --auto-tls' [$TLS]
      --tls-cert=              Path to TLS certificate (default: server.crt) [$TLS_CERT]
      --tls-key=               Path to TLS key (default: server.key) [$TLS_KEY]

Help Options:
  -h, --help                   Show this help message

[serve command options]
      -w, --work-dir=          Working directory [$WORK_DIR]
      -I, --disable-isolation  Disable isolated work dirs [$DISABLE_ISOLATION]
      -D, --enable-dot-files   Enable lookup for scripts in dor directories and files [$ENABLE_DOT_FILES]

[serve command arguments]
  Scripts:                     Scripts directory


```


Example:


**expose scripts in current dir**

```
wd serve .
```

in case there is a script `echo.sh` in the current directory, it will be available over `/echo.sh`.