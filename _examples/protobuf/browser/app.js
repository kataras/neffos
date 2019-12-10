const neffos = require('neffos.js');
const protos = require("./user_message_pb.js")

var scheme = document.location.protocol == "https:" ? "wss" : "ws";
var port = document.location.port ? ":" + document.location.port : "";
var wsURL = scheme + "://" + document.location.hostname + port + "/echo";

var outputTxt = document.getElementById("output");

function addMessage(msg) {
    outputTxt.innerHTML += msg + "\n";
}

function handleError(reason) {
    console.log(reason);
    window.alert(reason);
}

function handleNamespaceConnectedConn(nsConn) {
    var username = prompt("Please specify a username: ");

    let inputTxt = document.getElementById("input");
    let sendBtn = document.getElementById("sendBtn");

    sendBtn.disabled = false;
    sendBtn.onclick = function () {
        const input = inputTxt.value;
        inputTxt.value = "";
        const userMsg = new protos.UserMessage();
        userMsg.setUsername(username);
        userMsg.setText(input);

        const body = userMsg.serializeBinary()
        // let msg = new neffos.Message();
        // msg.Namespace = "default";
        // msg.Event = "chat";
        // msg.Body = body;
        // msg.SetBinary = true;
        // nsConn.conn.write(msg);
        // ^ == 
        nsConn.emitBinary("chat", body);
        //
        // OR: javascript side will check if body is binary,
        // and if it's it will convert it to valid utf-8 text before sending.
        // To keep the data as they are, please prefer `emitBinary`.
        // nsConn.emit("chat", body);
        addMessage("Me: " + input);
    };
}

async function runExample() {
    // You can omit the "default" and simply define only Events, the namespace will be an empty string"",
    // however if you decide to make any changes on this example make sure the changes are reflecting inside the ../server.go file as well.
    try {
        const conn = await neffos.dial(wsURL, {
            default: { // "default" namespace.
                _OnNamespaceConnected: function (nsConn, msg) {
                    addMessage("connected to namespace: " + msg.Namespace);
                    handleNamespaceConnectedConn(nsConn);
                },
                _OnNamespaceDisconnect: function (nsConn, msg) {
                    addMessage("disconnected from namespace: " + msg.Namespace);
                },
                chat: function (nsConn, msg) { // "chat" event.
                    console.log(msg);
                    const userMsg = protos.UserMessage.deserializeBinary(msg.Body);
                    addMessage(userMsg.getUsername() + ": " + userMsg.getText());
                },
                chat_test:  function (nsConn, msg) {
                    addMessage(new TextDecoder("utf-8").decode(msg.Body));
                }
            }
        });

        conn.connect("default");

    } catch (err) {
        handleError(err);
    }
}

runExample();