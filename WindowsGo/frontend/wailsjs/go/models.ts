export namespace appstate {
	
	export class FileEntry {
	    name: string;
	    display: string;
	    isDir: boolean;
	    size: number;
	    remote: string;
	
	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.display = source["display"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.remote = source["remote"];
	    }
	}
	export class OpenRequest {
	    kind: string;
	    localDir: string;
	    url: string;
	    user: string;
	    pass: string;
	    masterPassword: string;
	    recoveryCode: string;
	    filenameEnc: boolean;
	    vaultName: string;
	    vaultPath: string;
	    create: boolean;
	
	    static createFrom(source: any = {}) {
	        return new OpenRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.localDir = source["localDir"];
	        this.url = source["url"];
	        this.user = source["user"];
	        this.pass = source["pass"];
	        this.masterPassword = source["masterPassword"];
	        this.recoveryCode = source["recoveryCode"];
	        this.filenameEnc = source["filenameEnc"];
	        this.vaultName = source["vaultName"];
	        this.vaultPath = source["vaultPath"];
	        this.create = source["create"];
	    }
	}
	export class OpenVaultResult {
	    code: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenVaultResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	    }
	}
	export class SyncStatus {
	    running: boolean;
	    total: number;
	    done: number;
	    current: string;
	    synced: number;
	    failed: number;
	    errors: string[];
	
	    static createFrom(source: any = {}) {
	        return new SyncStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.total = source["total"];
	        this.done = source["done"];
	        this.current = source["current"];
	        this.synced = source["synced"];
	        this.failed = source["failed"];
	        this.errors = source["errors"];
	    }
	}
	export class Snapshot {
	    connected: boolean;
	    vaultName: string;
	    vaultPath: string;
	    backend: string;
	    backendId: string;
	    filenameEnc: boolean;
	    hasRecovery: boolean;
	    connectedSec: number;
	    resumeCount: number;
	    proxyBase: string;
	    autoLockMin: number;
	    transferActive: boolean;
	    transferDone: number;
	    transferTotal: number;
	    transferTasks: number;
	    sync: SyncStatus;
	    statsDone: boolean;
	    statsTotal: number;
	    statsFiles: number;
	    statsFailed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connected = source["connected"];
	        this.vaultName = source["vaultName"];
	        this.vaultPath = source["vaultPath"];
	        this.backend = source["backend"];
	        this.backendId = source["backendId"];
	        this.filenameEnc = source["filenameEnc"];
	        this.hasRecovery = source["hasRecovery"];
	        this.connectedSec = source["connectedSec"];
	        this.resumeCount = source["resumeCount"];
	        this.proxyBase = source["proxyBase"];
	        this.autoLockMin = source["autoLockMin"];
	        this.transferActive = source["transferActive"];
	        this.transferDone = source["transferDone"];
	        this.transferTotal = source["transferTotal"];
	        this.transferTasks = source["transferTasks"];
	        this.sync = this.convertValues(source["sync"], SyncStatus);
	        this.statsDone = source["statsDone"];
	        this.statsTotal = source["statsTotal"];
	        this.statsFiles = source["statsFiles"];
	        this.statsFailed = source["statsFailed"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class TaskView {
	    id: number;
	    name: string;
	    direction: string;
	    state: string;
	    progress: number;
	    totalBytes: number;
	    doneBytes: number;
	    errorMsg: string;
	    remote: string;
	    remoteDir: string;
	    local: string;
	
	    static createFrom(source: any = {}) {
	        return new TaskView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.direction = source["direction"];
	        this.state = source["state"];
	        this.progress = source["progress"];
	        this.totalBytes = source["totalBytes"];
	        this.doneBytes = source["doneBytes"];
	        this.errorMsg = source["errorMsg"];
	        this.remote = source["remote"];
	        this.remoteDir = source["remoteDir"];
	        this.local = source["local"];
	    }
	}

}

export namespace bind {
	
	export class BaiduAuthInfo {
	    authorized: boolean;
	    appKey: string;
	    appId: string;
	
	    static createFrom(source: any = {}) {
	        return new BaiduAuthInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.authorized = source["authorized"];
	        this.appKey = source["appKey"];
	        this.appId = source["appId"];
	    }
	}

}

