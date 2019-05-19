// start of javascript-based client.
// tsc --outDir ./_examples/browser client.ts
if (!("TextDecoder" in window) || !("TextEncoder" in window)) {
    throw new Error("this browser does not support Text Encoding/Decoding...");
}
var dec = new TextDecoder("utf-8");
var enc = new TextEncoder();
var OnNamespaceConnect = "_OnNamespaceConnect";
var OnNamespaceConnected = "_OnNamespaceConnected";
var OnNamespaceDisconnect = "_OnNamespaceDisconnect";
var OnRoomJoin = "_OnRoomJoin";
var OnRoomJoined = "_OnRoomJoined";
var OnRoomLeave = "_OnRoomLeave";
var OnRoomLeft = "_OnRoomLeft";
var OnAnyEvent = "_OnAnyEvent";
var OnNativeMessage = "_OnNativeMessage";
// see `handleAck`.
var ackIDBinary = 2; // comes from server to client after ackBinary and ready as a prefix, the rest message is the conn's ID.
var ackNotOKBinary = 4; // comes from server to client if `Server#OnConnected` errored as a prefix, the rest message is the error text.
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
var Message = /** @class */ (function () {
    function Message() {
    }
    Message.prototype.isConnect = function () {
        return this.Event == OnNamespaceConnect;
    };
    Message.prototype.isDisconnect = function () {
        return this.Event == OnNamespaceDisconnect;
    };
    Message.prototype.isRoomJoin = function () {
        return this.Event == OnRoomJoin;
    };
    Message.prototype.isRoomLeft = function () {
        return this.Event == OnRoomLeft;
    };
    return Message;
}());
var messageSeparator = ';';
var validMessageSepCount = 7;
var trueString = "1";
var falseString = "0";
function serializeMessage(msg) {
    if (msg.IsNative && isEmpty(msg.wait)) {
        return msg.Body;
    }
    var isErrorString = falseString;
    var isNoOpString = falseString;
    var waitString = msg.wait || "";
    var body = msg.Body || "";
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
    var dts = data.split(messageSeparator, validMessageSepCount);
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
    var body = dts[6];
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
var ErrInvalidPayload = "invalid payload";
// interface Events {
//     "error": Event;
//     "message": MessageEvent;
//     "open": Event;
// }
var Ws = /** @class */ (function () {
    // // listeners.
    // private errorListeners: (err:string)
    function Ws(endpoint, protocols) {
        var _this = this;
        if (!window["WebSocket"]) {
            return;
        }
        if (endpoint.indexOf("ws") == -1) {
            endpoint = "ws://" + endpoint;
        }
        this.conn = new WebSocket(endpoint, protocols);
        this.conn.binaryType = "arraybuffer";
        this.conn.onerror = (function (evt) {
            console.error("WebSocket error observed:", event);
        });
        this.conn.onopen = (function (evt) {
            console.log("WebSocket connected.");
            var b = new Uint8Array(1);
            b[0] = 1;
            _this.conn.send(b.buffer);
            return null;
        });
        this.conn.onclose = (function (evt) {
            console.log("WebSocket disconnected.");
            return null;
        });
        this.conn.onmessage = (function (evt) {
            console.log("WebSocket On Message.");
            if (!_this.isAcknowledged) {
                if (evt.data instanceof ArrayBuffer) {
                    var errorText = _this.handleAck(new Uint8Array(evt.data));
                    if (errorText == undefined) {
                        _this.isAcknowledged = true;
                        _this.handleQueue();
                    }
                    else {
                        _this.conn.close();
                        console.error(errorText);
                    }
                    return;
                }
                // let data = toWSData(evt.data)
                _this.queue.push(evt.data);
                return;
            }
            // let data = toWSData(evt.data); 
            console.log(evt.data);
            var err = _this.handleMessage(evt.data);
        });
    }
    Ws.prototype.handleAck = function (data) {
        var typ = data[0];
        switch (typ) {
            case ackIDBinary:
                var id = dec.decode(data.slice(1));
                this.ID = id;
                console.info("SET ID: ", id);
                break;
            case ackNotOKBinary:
                var errorText = dec.decode(data.slice(1));
                return errorText;
            default:
                return "";
        }
    };
    Ws.prototype.handleQueue = function () {
        var _this = this;
        if (this.queue == undefined || this.queue.length == 0) {
            return;
        }
        this.queue.forEach(function (item, index) {
            _this.queue.splice(index, 1);
            _this.handleMessage(item);
        });
    };
    Ws.prototype.handleMessage = function (data) {
        var msg = deserializeMessage(data, this.allowNativeMessages);
        if (msg.isInvalid) {
            return ErrInvalidPayload;
        }
        console.info(msg);
        // TODO: ...
        // it's a native websocket message
        // this.handleNativeMessage(data);
    };
    Ws.prototype.handleNativeMessage = function (data) {
        // console.log(data);
    };
    return Ws;
}());
