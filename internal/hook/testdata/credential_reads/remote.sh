#!/usr/bin/env bash
ssh -p 2222 fixture-host 'cat /srv/service/.env'
