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
	    td: number;
	
	    static createFrom(source: any = {}) {
	        return new ColorEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hex = source["hex"];
	        this.label = source["label"];
	        this.td = source["td"];
	    }
	}
	export class DebugCellsSlabResult {
	    svg: string;
	    slabCount: number;
	    medianCellAreaMM2: number;
	
	    static createFrom(source: any = {}) {
	        return new DebugCellsSlabResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.svg = source["svg"];
	        this.slabCount = source["slabCount"];
	        this.medianCellAreaMM2 = source["medianCellAreaMM2"];
	    }
	}
	export class LoadSettingsResult {
	    path: string;
	    settings: settings.Settings;
	
	    static createFrom(source: any = {}) {
	        return new LoadSettingsResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.settings = this.convertValues(source["settings"], settings.Settings);
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
	export class MaterialXOpenResult {
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new MaterialXOpenResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	    }
	}
	export class NozzleOption {
	    diameter: string;
	    layerHeights: number[];
	
	    static createFrom(source: any = {}) {
	        return new NozzleOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.diameter = source["diameter"];
	        this.layerHeights = source["layerHeights"];
	    }
	}
	export class PrinterOption {
	    id: string;
	    displayName: string;
	    nozzles: NozzleOption[];
	
	    static createFrom(source: any = {}) {
	        return new PrinterOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.displayName = source["displayName"];
	        this.nozzles = this.convertValues(source["nozzles"], NozzleOption);
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
	
	export class SplitPreviewResult {
	    origin: number[];
	    normal: number[];
	    u: number[];
	    v: number[];
	    halfExtentU: number;
	    halfExtentV: number;
	
	    static createFrom(source: any = {}) {
	        return new SplitPreviewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.origin = source["origin"];
	        this.normal = source["normal"];
	        this.u = source["u"];
	        this.v = source["v"];
	        this.halfExtentU = source["halfExtentU"];
	        this.halfExtentV = source["halfExtentV"];
	    }
	}
	export class SplitSettings {
	    Enabled: boolean;
	    Axis: number;
	    Offset: number;
	    ConnectorStyle: string;
	    ConnectorCount: number;
	    ConnectorDiamMM: number;
	    ConnectorDepthMM: number;
	    ClearanceMM: number;
	    Orientation: string[];
	
	    static createFrom(source: any = {}) {
	        return new SplitSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Enabled = source["Enabled"];
	        this.Axis = source["Axis"];
	        this.Offset = source["Offset"];
	        this.ConnectorStyle = source["ConnectorStyle"];
	        this.ConnectorCount = source["ConnectorCount"];
	        this.ConnectorDiamMM = source["ConnectorDiamMM"];
	        this.ConnectorDepthMM = source["ConnectorDepthMM"];
	        this.ClearanceMM = source["ClearanceMM"];
	        this.Orientation = source["Orientation"];
	    }
	}

}

export namespace settings {
	
	export class ColorSlotSetting {
	    hex: string;
	    label?: string;
	    collection?: string;
	    td?: number;
	
	    static createFrom(source: any = {}) {
	        return new ColorSlotSetting(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hex = source["hex"];
	        this.label = source["label"];
	        this.collection = source["collection"];
	        this.td = source["td"];
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
	    printer?: string;
	    nozzleDiameter: string;
	    layerHeight: string;
	    baseColor?: ColorSlotSetting;
	    baseMaterialXPath?: string;
	    baseMaterialXTileMM?: number;
	    baseMaterialXTriplanarSharpness?: number;
	    baseColorMode?: string;
	    colorSlots: ColorSlotSetting[];
	    inventoryCollection: string;
	    brightness: number;
	    contrast: number;
	    saturation: number;
	    warpPins: WarpPinSetting[];
	    stickers?: StickerSetting[];
	    dither: string;
	    riemersmaBias: number;
	    blueNoiseTol: number;
	    colorSnap: number;
	    noMerge: boolean;
	    noCellMerge: boolean;
	    noSimplify: boolean;
	    honorTD: boolean;
	    colorAwareCells: boolean;
	    colorRegionContrast: number;
	    stats: boolean;
	    showSampledColors: boolean;
	    alphaWrap: boolean;
	    alphaWrapAlpha: string;
	    alphaWrapOffset: string;
	    layer0AdhesionXYScale: number;
	    upperLayerXYScale: number;
	    splitEnabled: boolean;
	    splitAxis: number;
	    splitOffset: number;
	    splitConnectorStyle: string;
	    splitConnectorCount: number;
	    splitConnectorDiamMM: number;
	    splitConnectorDepthMM: number;
	    splitClearanceMM: number;
	    splitOrientationA: string;
	    splitOrientationB: string;
	
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
	        this.printer = source["printer"];
	        this.nozzleDiameter = source["nozzleDiameter"];
	        this.layerHeight = source["layerHeight"];
	        this.baseColor = this.convertValues(source["baseColor"], ColorSlotSetting);
	        this.baseMaterialXPath = source["baseMaterialXPath"];
	        this.baseMaterialXTileMM = source["baseMaterialXTileMM"];
	        this.baseMaterialXTriplanarSharpness = source["baseMaterialXTriplanarSharpness"];
	        this.baseColorMode = source["baseColorMode"];
	        this.colorSlots = this.convertValues(source["colorSlots"], ColorSlotSetting);
	        this.inventoryCollection = source["inventoryCollection"];
	        this.brightness = source["brightness"];
	        this.contrast = source["contrast"];
	        this.saturation = source["saturation"];
	        this.warpPins = this.convertValues(source["warpPins"], WarpPinSetting);
	        this.stickers = this.convertValues(source["stickers"], StickerSetting);
	        this.dither = source["dither"];
	        this.riemersmaBias = source["riemersmaBias"];
	        this.blueNoiseTol = source["blueNoiseTol"];
	        this.colorSnap = source["colorSnap"];
	        this.noMerge = source["noMerge"];
	        this.noCellMerge = source["noCellMerge"];
	        this.noSimplify = source["noSimplify"];
	        this.honorTD = source["honorTD"];
	        this.colorAwareCells = source["colorAwareCells"];
	        this.colorRegionContrast = source["colorRegionContrast"];
	        this.stats = source["stats"];
	        this.showSampledColors = source["showSampledColors"];
	        this.alphaWrap = source["alphaWrap"];
	        this.alphaWrapAlpha = source["alphaWrapAlpha"];
	        this.alphaWrapOffset = source["alphaWrapOffset"];
	        this.layer0AdhesionXYScale = source["layer0AdhesionXYScale"];
	        this.upperLayerXYScale = source["upperLayerXYScale"];
	        this.splitEnabled = source["splitEnabled"];
	        this.splitAxis = source["splitAxis"];
	        this.splitOffset = source["splitOffset"];
	        this.splitConnectorStyle = source["splitConnectorStyle"];
	        this.splitConnectorCount = source["splitConnectorCount"];
	        this.splitConnectorDiamMM = source["splitConnectorDiamMM"];
	        this.splitConnectorDepthMM = source["splitConnectorDepthMM"];
	        this.splitClearanceMM = source["splitClearanceMM"];
	        this.splitOrientationA = source["splitOrientationA"];
	        this.splitOrientationB = source["splitOrientationB"];
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

