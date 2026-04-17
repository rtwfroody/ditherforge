export namespace loader {
	
	export class ObjectInfo {
	    index: number;
	    name: string;
	    triCount: number;
	    thumbnail: string;
	
	    static createFrom(source: any = {}) {
	        return new ObjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.name = source["name"];
	        this.triCount = source["triCount"];
	        this.thumbnail = source["thumbnail"];
	    }
	}

}

export namespace main {
	
	export class CollectionInfo {
	    name: string;
	    count: number;
	    builtIn: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CollectionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	        this.builtIn = source["builtIn"];
	    }
	}
	export class ColorEntry {
	    hex: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ColorEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hex = source["hex"];
	        this.label = source["label"];
	    }
	}
	export class ColorSlotSetting {
	    hex: string;
	    label?: string;
	    collection?: string;
	
	    static createFrom(source: any = {}) {
	        return new ColorSlotSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hex = source["hex"];
	        this.label = source["label"];
	        this.collection = source["collection"];
	    }
	}
	export class StickerSetting {
	    imagePath: string;
	    center: number[];
	    normal: number[];
	    up: number[];
	    scale: number;
	    rotation: number;
	    maxAngle?: number;
	    mode?: string;
	
	    static createFrom(source: any = {}) {
	        return new StickerSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imagePath = source["imagePath"];
	        this.center = source["center"];
	        this.normal = source["normal"];
	        this.up = source["up"];
	        this.scale = source["scale"];
	        this.rotation = source["rotation"];
	        this.maxAngle = source["maxAngle"];
	        this.mode = source["mode"];
	    }
	}
	export class WarpPinSetting {
	    sourceHex: string;
	    targetHex: string;
	    targetLabel?: string;
	    sigma: number;
	
	    static createFrom(source: any = {}) {
	        return new WarpPinSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceHex = source["sourceHex"];
	        this.targetHex = source["targetHex"];
	        this.targetLabel = source["targetLabel"];
	        this.sigma = source["sigma"];
	    }
	}
	export class Settings {
	    inputFile?: string;
	    objectIndex?: number;
	    sizeMode: string;
	    sizeValue: string;
	    scaleValue: string;
	    nozzleDiameter: string;
	    layerHeight: string;
	    baseColor?: ColorSlotSetting;
	    colorSlots: ColorSlotSetting[];
	    inventoryCollection: string;
	    brightness: number;
	    contrast: number;
	    saturation: number;
	    warpPins: WarpPinSetting[];
	    stickers?: StickerSetting[];
	    dither: string;
	    colorSnap: number;
	    noMerge: boolean;
	    noSimplify: boolean;
	    stats: boolean;
	    alphaWrap: boolean;
	    alphaWrapAlpha: string;
	    alphaWrapOffset: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputFile = source["inputFile"];
	        this.objectIndex = source["objectIndex"];
	        this.sizeMode = source["sizeMode"];
	        this.sizeValue = source["sizeValue"];
	        this.scaleValue = source["scaleValue"];
	        this.nozzleDiameter = source["nozzleDiameter"];
	        this.layerHeight = source["layerHeight"];
	        this.baseColor = this.convertValues(source["baseColor"], ColorSlotSetting);
	        this.colorSlots = this.convertValues(source["colorSlots"], ColorSlotSetting);
	        this.inventoryCollection = source["inventoryCollection"];
	        this.brightness = source["brightness"];
	        this.contrast = source["contrast"];
	        this.saturation = source["saturation"];
	        this.warpPins = this.convertValues(source["warpPins"], WarpPinSetting);
	        this.stickers = this.convertValues(source["stickers"], StickerSetting);
	        this.dither = source["dither"];
	        this.colorSnap = source["colorSnap"];
	        this.noMerge = source["noMerge"];
	        this.noSimplify = source["noSimplify"];
	        this.stats = source["stats"];
	        this.alphaWrap = source["alphaWrap"];
	        this.alphaWrapAlpha = source["alphaWrapAlpha"];
	        this.alphaWrapOffset = source["alphaWrapOffset"];
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
	export class LoadSettingsResult {
	    path: string;
	    settings: Settings;
	
	    static createFrom(source: any = {}) {
	        return new LoadSettingsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.settings = this.convertValues(source["settings"], Settings);
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
	
	

}

export namespace pipeline {
	
	export class Sticker {
	    ImagePath: string;
	    Center: number[];
	    Normal: number[];
	    Up: number[];
	    Scale: number;
	    Rotation: number;
	    MaxAngle: number;
	    Mode: string;
	
	    static createFrom(source: any = {}) {
	        return new Sticker(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ImagePath = source["ImagePath"];
	        this.Center = source["Center"];
	        this.Normal = source["Normal"];
	        this.Up = source["Up"];
	        this.Scale = source["Scale"];
	        this.Rotation = source["Rotation"];
	        this.MaxAngle = source["MaxAngle"];
	        this.Mode = source["Mode"];
	    }
	}
	export class WarpPin {
	    sourceHex: string;
	    targetHex: string;
	    sigma: number;
	
	    static createFrom(source: any = {}) {
	        return new WarpPin(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sourceHex = source["sourceHex"];
	        this.targetHex = source["targetHex"];
	        this.sigma = source["sigma"];
	    }
	}
	export class Options {
	    Input: string;
	    NumColors: number;
	    LockedColors: string[];
	    Scale: number;
	    Output: string;
	    BaseColor: string;
	    NozzleDiameter: number;
	    LayerHeight: number;
	    InventoryFile: string;
	    InventoryColors?: number[][];
	    InventoryLabels?: string[];
	    Brightness: number;
	    Contrast: number;
	    Saturation: number;
	    Dither: string;
	    NoMerge: boolean;
	    NoSimplify: boolean;
	    Size?: number;
	    Force: boolean;
	    ReloadSeq: number;
	    Stats: boolean;
	    ColorSnap: number;
	    WarpPins?: WarpPin[];
	    Stickers?: Sticker[];
	    ObjectIndex: number;
	    AlphaWrap: boolean;
	    AlphaWrapAlpha: number;
	    AlphaWrapOffset: number;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Input = source["Input"];
	        this.NumColors = source["NumColors"];
	        this.LockedColors = source["LockedColors"];
	        this.Scale = source["Scale"];
	        this.Output = source["Output"];
	        this.BaseColor = source["BaseColor"];
	        this.NozzleDiameter = source["NozzleDiameter"];
	        this.LayerHeight = source["LayerHeight"];
	        this.InventoryFile = source["InventoryFile"];
	        this.InventoryColors = source["InventoryColors"];
	        this.InventoryLabels = source["InventoryLabels"];
	        this.Brightness = source["Brightness"];
	        this.Contrast = source["Contrast"];
	        this.Saturation = source["Saturation"];
	        this.Dither = source["Dither"];
	        this.NoMerge = source["NoMerge"];
	        this.NoSimplify = source["NoSimplify"];
	        this.Size = source["Size"];
	        this.Force = source["Force"];
	        this.ReloadSeq = source["ReloadSeq"];
	        this.Stats = source["Stats"];
	        this.ColorSnap = source["ColorSnap"];
	        this.WarpPins = this.convertValues(source["WarpPins"], WarpPin);
	        this.Stickers = this.convertValues(source["Stickers"], Sticker);
	        this.ObjectIndex = source["ObjectIndex"];
	        this.AlphaWrap = source["AlphaWrap"];
	        this.AlphaWrapAlpha = source["AlphaWrapAlpha"];
	        this.AlphaWrapOffset = source["AlphaWrapOffset"];
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
	

}

