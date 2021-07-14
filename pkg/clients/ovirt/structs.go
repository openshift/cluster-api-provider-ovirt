package ovirt

import ovirtsdk "github.com/ovirt/go-ovirt"

type ovirtClient struct {
	connection *ovirtsdk.Connection
}

type Instance struct {
	*ovirtsdk.Vm
}

type Creds struct {
	URL      string
	Username string
	Password string
	CAFile   string
	Insecure bool
	CABundle string
}
