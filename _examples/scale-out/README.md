# Scale Out Example

The only requirements are [docker](https://docs.docker.com/install/) and [docker-compose](https://docs.docker.com/compose/install/).

```sh
$ docker-compose up
```

It will start two neffos websocket servers publishing and subscribed to the same redis instance. Open your host browser and navigate to:

- http://localhost:8080
- http://localhost:9090

You will see that those servers are communicating between them and acting as one to the end-user.
