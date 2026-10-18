#!/usr/bin/env bash
docker exec fixture-container cat /run/secrets/SERVICE_PASSWORD
