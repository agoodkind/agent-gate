#!/usr/bin/env bash
curl --user "${SERVICE_USERNAME}:${SERVICE_PASSWORD}" "${SERVICE_ENDPOINT}/_cluster/health"
