#!/bin/sh
systemctl stop webhooks.service || echo "failed to stop service"
systemctl disable webhooks.service || echo "failed to disable service"
