package cache

type notificationType int

const (
	notificationTypeSet notificationType = iota // notificationTypeSet == 0
	notificationTypeDel
)

type NotificationMessage struct {
	NotificationType notificationType `json:"notification_type"`
	ObjectType       string           `json:"object_type"`
	Key              string           `json:"key"`
	Payload          string           `json:"payload"`
}

type ObjectType struct {
	Type interface{}
	TTL  int
}
