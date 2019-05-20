// start of javascript-based client.
// tsc --target es6 client.ts

// if (!("TextDecoder" in window) || !("TextEncoder" in window)) {
//     throw new Error("this browser does not support Text Encoding/Decoding...");
// }

// var dec: TextDecoder = new TextDecoder("utf-8");
// var enc: TextEncoder = new TextEncoder();

// type WSData = Uint8Array | string;

// var OnlyBinary: boolean = false;

// function toWSData(data: any): WSData {
//     if (data instanceof ArrayBuffer && !OnlyBinary) {
//         if (OnlyBinary) {
//             return new Uint8Array(data);
//         }
//         return dec.decode(data);
//     }

//     return data;
// }

type WSData = string;

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
const ackIDBinary = 'A';// comes from server to client after ackBinary and ready as a prefix, the rest message is the conn's ID.
const ackNotOKBinary = 'H'; // comes from server to client if `Server#OnConnected` errored as a prefix, the rest message is the error text.

const waitIsConfirmationPrefix = '#';
const waitComesFromClientPrefix = '$';

function IsSystemEvent(event: string): boolean {
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

function isEmpty(s: string): boolean {
    if (s === undefined) {
        return true
    }

    return s.length == 0 || s == "";
}

class Message {
    wait: string;

    Namespace: string;
    Room: string;
    Event: string;
    Body: WSData;
    Err: string;

    isError: boolean;
    isNoOp: boolean;

    isInvalid: boolean;

    from: string;

    IsForced: boolean;
    IsLocal: boolean;

    IsNative: boolean;

    isConnect(): boolean {
        return this.Event == OnNamespaceConnect || false;
    }

    isDisconnect(): boolean {
        return this.Event == OnNamespaceDisconnect || false;
    }


    isRoomJoin(): boolean {
        return this.Event == OnRoomJoin || false;
    }


    isRoomLeft(): boolean {
        return this.Event == OnRoomLeft || false;
    }

    isWait(): boolean {
        if (isEmpty(this.wait)) {
            return false;
        }

        if (this.wait[0] == waitIsConfirmationPrefix) {
            return true
        }

        return this.wait[0] == waitComesFromClientPrefix || false;
    }
}


const messageSeparator = ';';
const validMessageSepCount = 7;

const trueString = "1";
const falseString = "0";

function serializeMessage(msg: Message): WSData {
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
        isNoOpString = trueString
    }


    return [
        msg.wait,
        msg.Namespace,
        msg.Room,
        msg.Event,
        isErrorString,
        isNoOpString,
        body].join(messageSeparator);
}

// <wait>;
// <namespace>;
// <room>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
function deserializeMessage(data: WSData, allowNativeMessages: boolean): Message {
    var msg: Message = new Message();
    if (data.length == 0) {
        msg.isInvalid = true;
        return msg;
    }

    let dts = data.split(messageSeparator, validMessageSepCount);
    if (dts.length != validMessageSepCount) {
        if (!allowNativeMessages) {
            msg.isInvalid = true;
        } else {
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
        } else {
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

function genWait(): string {
    let now = window.performance.now();
    return waitIsConfirmationPrefix + now.toString();
}

function genWaitConfirmation(wait: string): string {
    return waitIsConfirmationPrefix + wait;
}

function genEmptyReplyToWait(wait: string): string {
    return wait + messageSeparator.repeat(validMessageSepCount - 1);
}

class NSConn {
    // TODO: ...
}



type MessageHandlerFunc = (c: NSConn, msg: Message) => Error;

// type Namespaces = Map<string, Events>;
// type Events = Map<string, MessageHandlerFunc>;


interface Events {
    [key: string]: MessageHandlerFunc;
}

function fireEvent(events: Events, ns: NSConn, msg: Message): Error {
    // let found = false;
    // let hasOnAnyEvent = false;
    // for (var key in events) {
    //     if (key === OnAnyEvent) {
    //         hasOnAnyEvent = true;
    //     }

    //     if (key !== msg.Event) {
    //         continue;
    //     }
    //     found = true
    //     let cb = events[key];
    //     return cb(ns, msg)
    // }

    // if (!found && hasOnAnyEvent) {
    //     return events[OnAnyEvent](ns, msg)
    // }

    if (events.hasOwnProperty(msg.Event)) {
        return events[msg.Event](ns, msg);
    }

    if (events.hasOwnProperty(OnAnyEvent)) {
        return events[OnAnyEvent](ns, msg);
    }

    return null;
}

interface Namespaces {
    [key: string]: Events;
}

function getEvents(namespaces: Namespaces, namespace: string): Events {
    if (namespaces.hasOwnProperty(namespace)) {
        return namespaces[namespace];
    }

    return null;
}

const ErrInvalidPayload = "invalid payload";
const ErrBadNamespace = "bad namespace"

type waitingMessageFunc = (msg: Message) => void;

class Ws {
    private conn: WebSocket;
    private dec: TextDecoder;
    private enc: TextEncoder;

    private isAcknowledged: boolean;
    private allowNativeMessages: boolean; // TODO: when events done fill it on constructor.

    ID: string;

    private queue: WSData[];
    private waitingMessages: Map<string, waitingMessageFunc>;
    private namespaces: Namespaces;
    private connectedNamespaces: Map<string, NSConn>;

    // // listeners.
    // private errorListeners: (err:string)

    constructor(endpoint: string, connHandler: Namespaces, protocols?: string[]) {
        if (!window["WebSocket"]) {
            return;
        }

        if (endpoint.indexOf("ws") == -1) {
            endpoint = "ws://" + endpoint;
        }

        if (connHandler === undefined) {
            return;
        }

        this.namespaces = connHandler;
        this.connectedNamespaces = new Map<string, NSConn>();

        this.waitingMessages = new Map<string, waitingMessageFunc>();

        this.conn = new WebSocket(endpoint, protocols);
        this.conn.binaryType = "arraybuffer";

        this.conn.onerror = ((evt: Event) => {
            console.error("WebSocket error observed:", event);
        });

        this.conn.onopen = ((evt: Event): any => {
            console.log("WebSocket connected.");
            // let b = new Uint8Array(1)
            // b[0] = 1;
            // this.conn.send(b.buffer);
            this.conn.send(ackBinary);
            return null;
        });

        this.conn.onclose = ((evt: Event): any => {
            console.log("WebSocket disconnected.");
            return null;
        });

        this.conn.onmessage = ((evt: MessageEvent) => {
            console.log("WebSocket On Message.");
            console.log(evt.data);
            if (!this.isAcknowledged) {
                // if (evt.data instanceof ArrayBuffer) {
                // new Uint8Array(evt.data)
                let errorText = this.handleAck(evt.data);
                if (errorText == undefined) {
                    this.isAcknowledged = true
                    this.handleQueue();
                } else {
                    this.conn.close();
                    console.error(errorText);
                }
                return;
            }



            let err = this.handleMessage(evt.data);
            if (!isEmpty(err)) {
                console.error(err) // TODO: remove this.
            }
        });
    }

    private handleAck(data: WSData): string {
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

    private handleQueue(): void {
        if (this.queue == undefined || this.queue.length == 0) {
            return;
        }

        this.queue.forEach((item, index) => {
            this.queue.splice(index, 1);
            this.handleMessage(item);
        });
    }

    private handleMessage(data: WSData): string {
        let msg = deserializeMessage(data, this.allowNativeMessages)
        if (msg.isInvalid) {
            return ErrInvalidPayload
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
                this.replyConnect(msg);
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
                    } else if (err instanceof String) {
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

    private handleNativeMessage(data: WSData): void {
        // console.log(data);
    }

    Namespace(namespace: string): NSConn {
        return this.connectedNamespaces.get(namespace)
    }


    private replyConnect(msg: Message): void {
        if (isEmpty(msg.wait) || msg.isNoOp) {
            return;
        }

        let ns = this.Namespace(msg.Namespace);
        if (ns !== undefined) {
            this.writeEmptyReply(msg.wait);
            return;
        }

        let events = getEvents(this.namespaces, msg.Namespace)
        if (events === undefined) {
            msg.Err = ErrBadNamespace;
            this.Write(msg);
            return;
        }

        // TODO: create new ns, fire event(on connect), write empty reply for wait and fire on connected and return.
    }

    IsClosed(): boolean {
        return this.conn.readyState == 3 || false;
    }

    Write(msg: Message): boolean {
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

    private write(data: any) {
        this.conn.send(data)
    }

    private writeEmptyReply(wait: string): void {
        this.write(genEmptyReplyToWait(wait));
    }
}
