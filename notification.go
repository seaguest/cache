package cache

import "time"

type notificationType int

const (
	notificationTypeSet notificationType = iota // notificationTypeSet == 0
	notificationTypeDel
)

type notificationMessage struct {
	NotificationType notificationType `json:"notification_type"`
	ObjectType       string           `json:"object_type"`
	Key              string           `json:"key"`
	Payload          string           `json:"payload"`
}

type objectType struct {
	Type interface{}
	TTL  time.Duration
}
