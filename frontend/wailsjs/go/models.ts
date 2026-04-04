export namespace pipeline {
	
	export class Options {
	    Input: string;
	    Palette: string;
	    AutoPalette?: number;
	    Scale: number;
	    Output: string;
	    NozzleDiameter: number;
	    LayerHeight: number;
	    InventoryFile: string;
	    Inventory?: number;
	    Dither: string;
	    NoMerge: boolean;
	    NoSimplify: boolean;
	    Size?: number;
	    Force: boolean;
	    Stats: boolean;
	    ColorSnap: number;
	    NoCache: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Options(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Input = source["Input"];
	        this.Palette = source["Palette"];
	        this.AutoPalette = source["AutoPalette"];
	        this.Scale = source["Scale"];
	        this.Output = source["Output"];
	        this.NozzleDiameter = source["NozzleDiameter"];
	        this.LayerHeight = source["LayerHeight"];
	        this.InventoryFile = source["InventoryFile"];
	        this.Inventory = source["Inventory"];
	        this.Dither = source["Dither"];
	        this.NoMerge = source["NoMerge"];
	        this.NoSimplify = source["NoSimplify"];
	        this.Size = source["Size"];
	        this.Force = source["Force"];
	        this.Stats = source["Stats"];
	        this.ColorSnap = source["ColorSnap"];
	        this.NoCache = source["NoCache"];
	    }
	}
	export class Result {
	    OutputPath: string;
	    FaceCount: number;
	    Duration: number;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.OutputPath = source["OutputPath"];
	        this.FaceCount = source["FaceCount"];
	        this.Duration = source["Duration"];
	    }
	}

}

