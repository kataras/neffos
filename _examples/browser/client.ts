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

function isEmpty(s: any): boolean {
    // if (s === undefined) {
    //     return true
    // }

    // return s.length == 0 || s == "";

    if (s === undefined) {
        return true
    }

    if (s === null) {
        return true
    }

    if (typeof s === 'string' || s instanceof String) {
        return s.length === 0 || s === "";
    }

    if (s instanceof Error) {
        return isEmpty(s.message);
    }

    return false;
}

function sleep(timeout: number) {
    return new Promise(resolve => setTimeout(resolve, timeout));
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
    let body = msg.Body || "";

    if (msg.isError) {
        body = msg.Err;
        isErrorString = trueString;
    }

    if (msg.isNoOp) {
        isNoOpString = trueString
    }

    return [
        msg.wait || "",
        msg.Namespace,
        msg.Room || "",
        msg.Event || "",
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
    } else {
        msg.Body = "";
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

class Room {
    nsConn: NSConn;
    name: string;


    constructor(ns: NSConn, roomName: string) {
        this.nsConn = ns;
        this.name = roomName;
    }

    Emit(event: string, body: WSData): boolean {
        let msg = new Message();
        msg.Namespace = this.nsConn.namespace;
        msg.Room = this.name;
        msg.Event = event;
        msg.Body = body;
        return this.nsConn.conn.Write(msg);
    }

    Leave(): Promise<Error> {
        let msg = new Message();
        msg.Namespace = this.nsConn.namespace;
        msg.Room = this.name;
        msg.Event = OnRoomLeave;
        return this.nsConn.askRoomLeave(msg);
    }
}

class NSConn {
    conn: Conn;
    namespace: string;
    events: Events;
    rooms: Map<string, Room>;

    constructor(conn: Conn, namespace: string, events: Events) {
        this.conn = conn;
        this.namespace = namespace;
        this.events = events;
        this.rooms = new Map<string, Room>();
    }

    Emit(event: string, body: WSData): boolean {
        let msg = new Message();
        msg.Namespace = this.namespace;
        msg.Event = event;
        msg.Body = body;
        return this.conn.Write(msg);
    }

    Ask(event: string, body: WSData): Promise<Message | Error> {
        let msg = new Message();
        msg.Namespace = this.namespace;
        msg.Event = event;
        msg.Body = body;
        return this.conn.Ask(msg);
    }

    JoinRoom(roomName: string): Promise<Room | Error> {
        return this.askRoomJoin(roomName);
    }

    Room(roomName: string): Room {
        return this.rooms.get(roomName);
    }

    Rooms(): Room[] {
        let rooms = new Array<Room>(this.rooms.size);
        this.rooms.forEach((room) => {
            rooms.push(room);
        })
        return rooms;
    }

    async LeaveAll(): Promise<Error> {
        let leaveMsg = new Message();
        leaveMsg.Namespace = this.namespace;
        leaveMsg.Event = OnRoomLeft;
        leaveMsg.IsLocal = true;

        this.rooms.forEach(async (value, roomName) => {
            leaveMsg.Room = roomName;
            try {
                await this.askRoomLeave(leaveMsg);
            } catch (err) {
                return err
            }
        })

        return null;
    }

    forceLeaveAll(isLocal: boolean) {
        let leaveMsg = new Message();
        leaveMsg.Namespace = this.namespace;
        leaveMsg.Event = OnRoomLeave;
        leaveMsg.IsForced = true;
        leaveMsg.IsLocal = isLocal;

        this.rooms.forEach((value, roomName) => {
            leaveMsg.Room = roomName;
            fireEvent(this, leaveMsg);

            this.rooms.delete(roomName);

            leaveMsg.Event = OnRoomLeft;
            fireEvent(this, leaveMsg);

            leaveMsg.Event = OnRoomLeave;
        });
    }

    Disconnect(): Promise<Error> {
        let disconnectMsg = new Message();
        disconnectMsg.Namespace = this.namespace;
        disconnectMsg.Event = OnNamespaceDisconnect;
        return this.conn.askDisconnect(disconnectMsg);
    }


    async askRoomJoin(roomName: string): Promise<Room | Error> {
        let room = this.rooms.get(roomName);
        if (room !== undefined) {
            return room;
        }

        let joinMsg = new Message();
        joinMsg.Namespace = this.namespace;
        joinMsg.Room = roomName;
        joinMsg.Event = OnRoomJoin;
        joinMsg.IsLocal = true;

        try {
            await this.conn.Ask(joinMsg);
        } catch (err) {
            return err;
        }

        let err = fireEvent(this, joinMsg);
        if (!isEmpty(err)) {
            return err;
        }

        room = new Room(this, roomName);
        this.rooms.set(roomName, room);

        joinMsg.Event = OnRoomJoined;
        fireEvent(this, joinMsg);
        return room;
    }

    async askRoomLeave(msg: Message): Promise<Error> {
        if (!this.rooms.has(msg.Room)) {
            return ErrBadRoom;
        }

        try {
            await this.conn.Ask(msg)
        } catch (err) {
            return err;
        }

        let err = fireEvent(this, msg);
        if (!isEmpty(err)) {
            return err;
        }

        this.rooms.delete(msg.Room);

        msg.Event = OnRoomLeft;
        fireEvent(this, msg);
        return null;
    }

    replyRoomJoin(msg: Message): void {
        if (isEmpty(msg.wait) || msg.isNoOp) {
            return;
        }

        if (!this.rooms.has(msg.Room)) {
            let err = fireEvent(this, msg);
            if (!isEmpty(err)) {
                msg.Err = err.message;
                this.conn.Write(msg);
                return;
            }

            this.rooms.set(msg.Room, new Room(this, msg.Room));

            msg.Event = OnRoomJoined;
            fireEvent(this, msg);
        }

        this.conn.writeEmptyReply(msg.wait);
    }

    replyRoomLeave(msg: Message): void {
        if (isEmpty(msg.wait) || msg.isNoOp) {
            return;
        }

        if (!this.rooms.has(msg.Room)) {
            this.conn.writeEmptyReply(msg.wait);
            return;
        }

        fireEvent(this, msg);

        this.rooms.delete(msg.Room);
        this.conn.writeEmptyReply(msg.wait);

        msg.Event = OnRoomLeft;
        fireEvent(this, msg);
    }
}


type MessageHandlerFunc = (c: NSConn, msg: Message) => Error;

// type Namespaces = Map<string, Events>;
// type Events = Map<string, MessageHandlerFunc>;


interface Events {
    [key: string]: MessageHandlerFunc;
}

function fireEvent(ns: NSConn, msg: Message): Error {
    if (ns.events.hasOwnProperty(msg.Event)) {
        return ns.events[msg.Event](ns, msg);
    }

    if (ns.events.hasOwnProperty(OnAnyEvent)) {
        return ns.events[OnAnyEvent](ns, msg);
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

const ErrInvalidPayload = new Error("invalid payload");
const ErrBadNamespace = new Error("bad namespace");
const ErrBadRoom = new Error("bad room");
const ErrClosed = new Error("use of closed connection");
const ErrWrite = new Error("write closed");

type waitingMessageFunc = (msg: Message) => void;

function Dial(endpoint: string, connHandler: Namespaces, protocols?: string[]): Promise<Conn> {
    if (endpoint.indexOf("ws") == -1) {
        endpoint = "ws://" + endpoint;
    }

    return new Promise((resolve, reject) => {
        if (!window["WebSocket"]) {
            reject("WebSocket is not accessible through this browser.");
        }

        if (connHandler === undefined) {
            reject("connHandler is empty.");
        }

        let ws = new WebSocket(endpoint, protocols);
        let conn = new Conn(ws, connHandler, protocols);
        ws.binaryType = "arraybuffer";
        ws.onmessage = ((evt: MessageEvent) => {
            console.log("WebSocket On Message.");
            console.log(evt.data);
            let err = conn.handle(evt);
            if (!isEmpty(err)) {
                reject(err);
                return;
            }

            if (conn.IsAcknowledged()) {
                // console.log("is acked, set new message handler");
                // conn.onmessage = ws.handle;
                resolve(conn);
            }
        });
        ws.onopen = ((evt: Event) => {
            console.log("WebSocket connected.");
            // let b = new Uint8Array(1)
            // b[0] = 1;
            // this.conn.send(b.buffer);
            ws.send(ackBinary);
        });
        ws.onerror = ((err: Event) => {
            conn.Close();
            reject(err);
        });
    });
}

class Conn {
    private conn: WebSocket;

    private isAcknowledged: boolean;
    private allowNativeMessages: boolean; // TODO: when events done fill it on constructor.

    ID: string;
    closed: boolean;

    private queue: WSData[];
    private waitingMessages: Map<string, waitingMessageFunc>;
    private namespaces: Namespaces;
    private connectedNamespaces: Map<string, NSConn>;
    private isConnectingProcesseses: string[]; // if elem exists then any receive of that namespace is locked until `askConnect` finished.
    // // listeners.
    // private errorListeners: (err:string)

    constructor(conn: WebSocket, connHandler: Namespaces, protocols?: string[]) {
        this.conn = conn;
        this.isAcknowledged = false;
        let hasEmptyNS = connHandler.hasOwnProperty("");
        this.allowNativeMessages = hasEmptyNS && connHandler[""].hasOwnProperty(OnNativeMessage);
        this.queue = new Array<string>();
        this.waitingMessages = new Map<string, waitingMessageFunc>();
        this.namespaces = connHandler;
        this.connectedNamespaces = new Map<string, NSConn>();
        this.isConnectingProcesseses = new Array<string>();
        this.closed = false;
        // this.conn = new WebSocket(endpoint, protocols);
        // this.conn.binaryType = "arraybuffer";

        // this.conn.onerror = ((evt: Event) => {
        //     console.error("WebSocket error observed:", event);
        // });

        this.conn.onclose = ((evt: Event): any => {
            console.log("WebSocket disconnected.");
            this.Close();
            return null;
        });

        // this.conn.onmessage = ((evt: MessageEvent) => {
        //     console.log("WebSocket On Message.");
        //     console.log(evt.data);
        //     let err = this.handle(evt.data);
        //     if (!isEmpty(err)) {
        //         console.error(err) // TODO: remove this.
        //     }
        // });
    }

    IsAcknowledged(): boolean {
        return this.isAcknowledged;
    }

    handle(evt: MessageEvent): Error {
        console.log("WebSocket Handle: ");
        console.log(evt.data);

        if (!this.isAcknowledged) {
            // if (evt.data instanceof ArrayBuffer) {
            // new Uint8Array(evt.data)
            let err = this.handleAck(evt.data);
            if (err == undefined) {
                console.log("SET ACK = TRUE");
                this.isAcknowledged = true
                this.handleQueue();
            } else {
                this.conn.close();
            }
            return err;
        }

        return this.handleMessage(evt.data);
    }

    private handleAck(data: WSData): Error {
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
                return new Error(errorText);
            default:
                this.queue.push(data);
                return null;
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

    private handleMessage(data: WSData): Error {
        let msg = deserializeMessage(data, this.allowNativeMessages)
        if (msg.isInvalid) {
            return ErrInvalidPayload
        }

        console.info(msg);


        if (msg.IsNative && this.allowNativeMessages) {
            let ns = this.Namespace("")
            return fireEvent(ns, msg);
        }

        if (msg.isWait()) {
            let cb = this.waitingMessages.get(msg.wait);
            if (cb != undefined) {
                cb(msg);
                return;
            }
        }

        const ns = this.Namespace(msg.Namespace);

        switch (msg.Event) {
            case OnNamespaceConnect:
                this.replyConnect(msg);
                break;
            case OnNamespaceDisconnect:
                this.replyDisconnect(msg);
                break;
            case OnRoomJoin:
                if (ns !== undefined) {
                    ns.replyRoomJoin(msg);
                    break;
                }
            case OnRoomLeave:
                if (ns !== undefined) {
                    ns.replyRoomLeave(msg);
                    break;
                }
            default:
                // this.checkWaitForNamespace(msg.Namespace);
                if (ns === undefined) {
                    return ErrBadNamespace;
                }
                msg.IsLocal = false;
                let err = fireEvent(ns, msg);
                if (!isEmpty(err)) {
                    // write any error back to the server.
                    msg.Err = err.message;
                    this.Write(msg);
                    return err;
                }
        }

        // it's a native websocket message
        // this.handleNativeMessage(data);
        return null;
    }

    Connect(namespace: string): Promise<NSConn | Error> {
        // for (; !this.isAcknowledged;) { await sleep(1000); }
        return this.askConnect(namespace);
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
            msg.Err = ErrBadNamespace.message;
            this.Write(msg);
            return;
        }

        ns = new NSConn(this, msg.Namespace, events);

        this.connectedNamespaces.set(msg.Namespace, ns);
        this.writeEmptyReply(msg.wait);

        msg.Event = OnNamespaceConnected;
        fireEvent(ns, msg);
    }

    private replyDisconnect(msg: Message): void {
        if (isEmpty(msg.wait) || msg.isNoOp) {
            return;
        }

        let ns = this.Namespace(msg.Namespace);
        if (ns === undefined) {
            this.writeEmptyReply(msg.wait);
            return;
        }

        ns.forceLeaveAll(true);

        this.connectedNamespaces.delete(msg.Namespace);

        this.writeEmptyReply(msg.wait);

        fireEvent(ns, msg);
    }

    Ask(msg: Message): Promise<Message | Error> {
        return new Promise((resolve, reject) => {
            if (this.IsClosed()) {
                reject(ErrClosed);
                return;
            }

            msg.wait = genWait();


            this.waitingMessages.set(msg.wait, ((receive: Message): void => {
                if (receive.isError) {
                    reject(new Error(receive.Err));
                    return;
                }
                resolve(receive);
            }));

            if (!this.Write(msg)) {
                reject(ErrWrite);
                return;
            }
        });
    }

    private addConnectProcess(namespace: string) {
        this.isConnectingProcesseses.push(namespace);
    }

    private removeConnectProcess(namespace: string) {
        let idx = this.isConnectingProcesseses.findIndex((value: string, index: number, obj) => { return value === namespace || false; });
        if (idx !== -1) {
            this.isConnectingProcesseses.splice(idx, 1);
        }
    }

    // no...
    // private async checkWaitForNamespace(namespace: string) {
    //     for (; this.hasConnectProcess(namespace);) {
    //         await sleep(1000); // wait 1 second before the loop circle.
    //     }
    // }

    private hasConnectProcess(namespace: string): boolean {
        let idx = this.isConnectingProcesseses.findIndex((value: string, index: number, obj) => { return value === namespace || false; });
        return idx !== -1;
    }

    private async askConnect(namespace: string): Promise<NSConn | Error> {
        let ns = this.Namespace(namespace);
        if (ns !== undefined) { // it's already connected.
            return ns;
        }

        let events = getEvents(this.namespaces, namespace);
        if (events === undefined) {
            return ErrBadNamespace;
        }

        this.addConnectProcess(namespace);
        let connectMessage = new Message()
        connectMessage.Namespace = namespace;
        connectMessage.Event = OnNamespaceConnect;
        connectMessage.IsLocal = true;

        this.isConnectingProcesseses.push(namespace);

        ns = new NSConn(this, namespace, events);
        let err = fireEvent(ns, connectMessage);
        if (!isEmpty(err)) {
            this.removeConnectProcess(namespace);
            return err;
        }

        try {
            await this.Ask(connectMessage);
        } catch (err) {
            return err;
        }

        this.connectedNamespaces.set(namespace, ns);

        connectMessage.Event = OnNamespaceConnected;
        fireEvent(ns, connectMessage);

        this.removeConnectProcess(namespace);

        return ns;
    }

    async askDisconnect(msg: Message): Promise<Error> {
        let ns = this.Namespace(msg.Namespace);
        if (ns === undefined) { // it's already connected.
            return ErrBadNamespace;
        }

        try {
            await this.Ask(msg);
        } catch (err) {
            return err
        }

        ns.forceLeaveAll(true);

        this.connectedNamespaces.delete(msg.Namespace);

        msg.IsLocal = true
        return fireEvent(ns, msg);
    }

    IsClosed(): boolean {
        return this.closed || this.conn.readyState == this.conn.CLOSED || false;
    }

    Write(msg: Message): boolean {
        if (this.IsClosed()) {
            console.log("is closed");
            return false;
        }

        if (!msg.isConnect() && !msg.isDisconnect()) {
            // namespace pre-write check.
            let ns = this.Namespace(msg.Namespace);

            if (ns === undefined) {
                console.error("namespace does not exist", msg.Namespace);
                return false;
            }

            // room per-write check.
            if (!isEmpty(msg.Room) && !msg.isRoomJoin() && !msg.isRoomLeft()) {
                if (!ns.rooms.has(msg.Room)) {
                    console.error("room does not exist", msg.Room)
                    // tried to send to a not joined room.
                    return false;
                }
            }
        }

        this.write(serializeMessage(msg));

        return true;
    }

    private write(data: any) {
        console.info("writing: ", data);
        this.conn.send(data)
    }

    writeEmptyReply(wait: string): void {
        this.write(genEmptyReplyToWait(wait));
    }

    Close(): void {
        if (this.closed) {
            return;
        }

        let disconnectMsg = new Message();
        disconnectMsg.Event = OnNamespaceDisconnect;
        disconnectMsg.IsForced = true;
        disconnectMsg.IsLocal = true;

        this.connectedNamespaces.forEach((ns) => {
            ns.forceLeaveAll(true);

            disconnectMsg.Namespace = ns.namespace;
            fireEvent(ns, disconnectMsg);
            this.connectedNamespaces.delete(ns.namespace);
        })

        this.waitingMessages.clear();

        if (this.conn.readyState === this.conn.OPEN) {
            this.conn.close();
        }

        this.closed = true;
    }
}
