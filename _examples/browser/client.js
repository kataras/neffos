// start of javascript-based client.
// tsc --target es6 --outDir ./_examples/browser client.ts
const OnNamespaceConnect = "_OnNamespaceConnect";
const OnNamespaceConnected = "_OnNamespaceConnected";
const OnNamespaceDisconnect = "_OnNamespaceDisconnect";
const OnRoomJoin = "_OnRoomJoin";
const OnRoomJoined = "_OnRoomJoined";
const OnRoomLeave = "_OnRoomLeave";
const OnRoomLeft = "_OnRoomLeft";
const OnAnyEvent = "_OnAnyEvent";
const OnNativeMessage = "_OnNativeMessage";
const ackBinary = 'M'; // see `onopen`, comes from client to server at startup.
// see `handleAck`.
const ackIDBinary = 'A'; // comes from server to client after ackBinary and ready as a prefix, the rest message is the conn's ID.
const ackNotOKBinary = 'H'; // comes from server to client if `Server#OnConnected` errored as a prefix, the rest message is the error text.
const waitIsConfirmationPrefix = '#';
const waitComesFromClientPrefix = '$';
function IsSystemEvent(event) {
    switch (event) {
        case OnNamespaceConnect:
        case OnNamespaceConnected:
        case OnNamespaceDisconnect:
        case OnRoomJoin:
        case OnRoomJoined:
        case OnRoomLeave:
        case OnRoomLeft:
            return true;
        default:
            return false;
    }
}
class Message {
    isConnect() {
        return this.Event == OnNamespaceConnect || false;
    }
    isDisconnect() {
        return this.Event == OnNamespaceDisconnect || false;
    }
    isRoomJoin() {
        return this.Event == OnRoomJoin || false;
    }
    isRoomLeft() {
        return this.Event == OnRoomLeft || false;
    }
    isWait() {
        if (isEmpty(this.wait)) {
            return false;
        }
        if (this.wait[0] == waitIsConfirmationPrefix) {
            return true;
        }
        return this.wait[0] == waitComesFromClientPrefix || false;
    }
}
const messageSeparator = ';';
const validMessageSepCount = 7;
const trueString = "1";
const falseString = "0";
function serializeMessage(msg) {
    if (msg.IsNative && isEmpty(msg.wait)) {
        return msg.Body;
    }
    let isErrorString = falseString;
    let isNoOpString = falseString;
    let waitString = msg.wait || "";
    let body = msg.Body || "";
    if (msg.isError) {
        body = msg.Err;
        isErrorString = trueString;
    }
    if (msg.isNoOp) {
        isNoOpString = trueString;
    }
    return [
        msg.wait,
        msg.Namespace,
        msg.Room,
        msg.Event,
        isErrorString,
        isNoOpString,
        body
    ].join(messageSeparator);
}
// <wait>;
// <namespace>;
// <room>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
function deserializeMessage(data, allowNativeMessages) {
    var msg = new Message();
    if (data.length == 0) {
        msg.isInvalid = true;
        return msg;
    }
    let dts = data.split(messageSeparator, validMessageSepCount);
    if (dts.length != validMessageSepCount) {
        if (!allowNativeMessages) {
            msg.isInvalid = true;
        }
        else {
            msg.Event = OnNativeMessage;
            msg.Body = data;
        }
        return msg;
    }
    msg.wait = dts[0];
    msg.Namespace = dts[1];
    msg.Room = dts[2];
    msg.Event = dts[3];
    msg.isError = dts[4] == trueString || false;
    msg.isNoOp = dts[5] == trueString || false;
    let body = dts[6];
    if (!isEmpty(body)) {
        if (msg.isError) {
            msg.Err = body;
        }
        else {
            msg.Body = body;
        }
    }
    msg.isInvalid = false;
    msg.IsForced = false;
    msg.IsLocal = false;
    msg.IsNative = (allowNativeMessages && msg.Event == OnNativeMessage) || false;
    // msg.SetBinary = false;
    return msg;
}
function isEmpty(s) {
    if (s == undefined) {
        return true;
    }
    return s.length == 0 || s == "";
}
class NSConn {
}
// interface Events {
//     "error": Event;
//     "message": MessageEvent;
//     "open": Event;
// }
const ErrInvalidPayload = "invalid payload";
class Ws {
    // // listeners.
    // private errorListeners: (err:string)
    constructor(endpoint, protocols) {
        if (!window["WebSocket"]) {
            return;
        }
        if (endpoint.indexOf("ws") == -1) {
            endpoint = "ws://" + endpoint;
        }
        this.waitingMessages = new Map();
        this.conn = new WebSocket(endpoint, protocols);
        this.conn.binaryType = "arraybuffer";
        this.conn.onerror = ((evt) => {
            console.error("WebSocket error observed:", event);
        });
        this.conn.onopen = ((evt) => {
            console.log("WebSocket connected.");
            // let b = new Uint8Array(1)
            // b[0] = 1;
            // this.conn.send(b.buffer);
            this.conn.send(ackBinary);
            return null;
        });
        this.conn.onclose = ((evt) => {
            console.log("WebSocket disconnected.");
            return null;
        });
        this.conn.onmessage = ((evt) => {
            console.log("WebSocket On Message.");
            console.log(evt.data);
            if (!this.isAcknowledged) {
                // if (evt.data instanceof ArrayBuffer) {
                // new Uint8Array(evt.data)
                let errorText = this.handleAck(evt.data);
                if (errorText == undefined) {
                    this.isAcknowledged = true;
                    this.handleQueue();
                }
                else {
                    this.conn.close();
                    console.error(errorText);
                }
                return;
            }
            let err = this.handleMessage(evt.data);
            if (!isEmpty(err)) {
                console.error(err); // TODO: remove this.
            }
        });
    }
    handleAck(data) {
        let typ = data[0];
        switch (typ) {
            case ackIDBinary:
                // let id = dec.decode(data.slice(1));
                let id = data.slice(1);
                this.ID = id;
                console.info("SET ID: ", id);
                break;
            case ackNotOKBinary:
                // let errorText = dec.decode(data.slice(1));
                let errorText = data.slice(1);
                return errorText;
            default:
                this.queue.push(data);
                return "";
        }
    }
    handleQueue() {
        if (this.queue == undefined || this.queue.length == 0) {
            return;
        }
        this.queue.forEach((item, index) => {
            this.queue.splice(index, 1);
            this.handleMessage(item);
        });
    }
    handleMessage(data) {
        let msg = deserializeMessage(data, this.allowNativeMessages);
        if (msg.isInvalid) {
            return ErrInvalidPayload;
        }
        console.info(msg);
        // TODO: ...
        if (msg.isWait()) {
            let cb = this.waitingMessages.get(msg.wait);
            if (cb != undefined) {
                cb(msg);
                return;
            }
        }
        switch (msg.Event) {
            case OnNamespaceConnect:
                break;
            case OnNamespaceDisconnect:
                break;
            case OnRoomJoin:
                break;
            case OnRoomLeave:
                break;
            default:
                // TODO: ...
                // let err = call_the_event_here
                let err = () => { };
                if (err != undefined && err != null) {
                    // write any error back to the server.
                    let errorText;
                    if (err instanceof Error) {
                        errorText = err.message;
                    }
                    else if (err instanceof String) {
                        errorText = err;
                    }
                    msg.Err = errorText;
                    this.Write(msg);
                    return errorText;
                }
        }
        // it's a native websocket message
        // this.handleNativeMessage(data);
    }
    handleNativeMessage(data) {
        // console.log(data);
    }
    IsClosed() {
        return this.conn.readyState == 3 || false;
    }
    Write(msg) {
        if (this.IsClosed()) {
            return false;
        }
        if (!msg.isConnect() && !msg.isDisconnect()) {
            // TODO: checks...
        }
        let send = serializeMessage(msg);
        console.info("writing: ", send);
        this.write(send);
    }
    write(data) {
        this.conn.send(data);
    }
}
