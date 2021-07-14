package ovirt

import (
	ovirtsdk "github.com/ovirt/go-ovirt"
)

func NewOvirtClient(conn *ovirtsdk.Connection) Client {
	return &ovirtClient{
		connection: conn,
	}
}
