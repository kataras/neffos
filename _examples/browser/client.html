<!-- the message's input -->
<input id="input" type="text" />

<!-- when clicked then a websocket event will be sent to the server, at this example we registered the 'chat' -->
<button onclick="send()">Send</button>

<!-- the messages will be shown here -->
<pre id="output"></pre>
<!-- import the client-side library for browser-->
<script src="/client.js"></script>

<script>
  var scheme = document.location.protocol == "https:" ? "wss" : "ws";
  var port = document.location.port ? ":" + document.location.port : "";

  var wsURL = scheme + "://" + document.location.hostname + port + "/echo";

  var input = document.getElementById("input");
  var output = document.getElementById("output");

  async function runExample() {
    let ws = await Dial(wsURL, {
      default: {
        _OnNamespaceConnected: function(c, msg) {
          console.info("connected to ", msg.Namespace);
        },
        _OnNamespaceDisconnect: function(c, msg) {
          console.info("disconnected from ", msg.Namespace);
        },
        chat: function(c, msg) {
          console.info("on chat: " + msg.Body);
        }
      }
    });

    await ws.Connect("default");
  }

  runExample();
  // OR:
  // Dial(wsURL, {
  //   default: {
  //     _OnNamespaceConnected: function(c, msg) {
  //       console.info("connected to ", msg.Namespace);
  //     },
  //     _OnNamespaceDisconnect: function(c, msg) {
  //       console.info("disconnected from ", msg.Namespace);
  //     },
  //     chat: function(c, msg) {
  //       console.info("on chat: " + msg.Body);
  //     }
  //   }
  // })
  //   .then(function(socket) {
  //     socket.Connect("default");
  //   })
  //   .catch(function(err) {
  //     console.error("WebSocket error observed:", err);
  //   });

  function send() {
    addMessage("Me: " + input.value); // write ourselves
    // TODO: send here.
    input.value = ""; // clear the input
  }

  function addMessage(msg) {
    output.innerHTML += msg + "\n";
  }
</script>
