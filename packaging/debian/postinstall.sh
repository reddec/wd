#!/bin/sh

systemctl enable webhooks.service || echo "failed to enable service"
systemctl start webhooks.service || echo "failed to start service"
mkdir -p /var/webhooks
chmod a+rw /var/webhooks