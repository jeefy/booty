# Booty

A simple (i)PXE Server for booting Flatcar-Linux

## Features

* PXE boot into the latest Flatcar-Linux
* MAC address based hostnames
* Automatic conversion of Container Linux Config to Ignition JSON
* JSON "Hardware Database" (right now just a MAC-to-hostname mapping)
* Automatic updates retrieved from Flatcar-Linux
* Automatic drain/reboot of nodes (in conjunction with [Kured](https://github.com/weaveworks/kured))

## Examples

### Docker

```
TODO
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
