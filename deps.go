package main

func EnsureDeps() {
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20210731/images/netboot/pxelinux.0")
	DownloadFile("http://ftp.us.debian.org/debian/dists/stable/main/installer-amd64/20210731/images/netboot/debian-installer/amd64/boot-screens/ldlinux.c32")
}
