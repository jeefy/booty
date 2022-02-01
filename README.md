# Booty

A simple (i)PXE Server for booting Flatcar-Linux

```
> booty --help

Easy iPXE server for Flatcar

Usage:
  booty [flags]

Flags:
      --architecture string     Architecture to use for the iPXE server (default "amd64")
      --channel string          Flatcar channel to look for updates (default "stable")
      --dataDir string          Directory to store stateful data (default "/data")
      --debug                   Enable debug logging
  -h, --help                    help for booty
      --httpPort int            Port to use for the HTTP server (default 8080)
      --serverIP string         IP address that clients can connect to (default "127.0.0.1")
      --updateSchedule string   Cron schedule to use for cleaning up cache files (default "* */1 * * *")
```

## Features

* PXE boot into the latest Flatcar-Linux
* MAC address based hostnames
* Automatic conversion of Container Linux Config to Ignition JSON
* JSON "Hardware Database" (right now just a MAC-to-hostname mapping)
* Automatic updates retrieved from Flatcar-Linux
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))


## Examples

[Example ignition config / helper scripts](examples/README.md)

### Docker

```
docker run --rm -it \
-v $PWD:/data/ \
-p 69:69/udp \
-p 80:8080 \
jeefy/booty:latest \
--dataDir=/storage/ \
--serverIP=192.168.1.10
```

### Kubernetes

```
TODO
```

## Additional Thoughts

**Why?**

I like treating (most of) my machines like cattle. This is an easier and more lightweight way to tackle PXE booting and patch management.

**Can you make it do X?**

Feature requests / optimizations / PRs are welcome! Feel free to ping me [@jeefy](https://twitter.com/jeefy) on Twitter.
