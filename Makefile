include .env

SHELL := bash.exe

export ROOT_DIR=$(shell pwd)

env-build:
	docker-compose build 
env-up:
	docker-compose up -d
env-down:
	docker-compose down
env-cleanup:
	read -p "Are you sure you want to delete all data? [y/n]:" ans; \
	if [ "$$ans" = "y" ] || [ "$$ans" = "Y" ]; then \
		docker-compose down -v &&\
		rm -rf ./data && \
		rm -rf ./out; \
	fi